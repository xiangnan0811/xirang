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
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"
)

func TestCryptoSensitiveBuffersRegisterZeroizationAtEachOwnershipBoundary(t *testing.T) {
	source, err := os.ReadFile("crypto.go")
	if err != nil {
		t.Fatalf("read crypto source: %v", err)
	}

	for _, check := range []struct {
		function string
		want     string
	}{
		{function: "encryptSelectionPath", want: "defer clear(subkey)"},
		{function: "decryptSelectionPath", want: "defer clear(subkey)"},
		{function: "newExportAEAD", want: "defer clear(subkey)"},
		{function: "UnwrapJobDEK", want: "clear(plaintext)"},
	} {
		function := cryptoSourceFunction(t, string(source), check.function)
		if !strings.Contains(function, check.want) {
			t.Fatalf("%s does not register sensitive-buffer cleanup %q", check.function, check.want)
		}
	}
}

func cryptoSourceFunction(t *testing.T, source, name string) string {
	t.Helper()
	start := strings.Index(source, "func "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		return source[start:]
	}
	return source[start : start+1+end]
}

func TestSelectionMetadataSubkeysClearOnSuccessAndErrorPaths(t *testing.T) {
	recorder := installCryptoClearRecorder(t)
	dek := bytes.Repeat([]byte{7}, 32)
	jobID := strings.Repeat("1", 32)
	itemID := strings.Repeat("2", 32)
	selectionDigest := strings.Repeat("a", 64)

	nonce, ciphertext, err := encryptSelectionPath(dek, jobID, itemID, selectionDigest, []string{"root", "report.txt"})
	if err != nil {
		t.Fatalf("encrypt selection path: %v", err)
	}
	if _, err := decryptSelectionPath(dek, jobID, itemID, selectionDigest, nonce, ciphertext); err != nil {
		t.Fatalf("decrypt selection path: %v", err)
	}
	if _, err := decryptSelectionPath(dek, jobID, itemID, selectionDigest, nonce[:len(nonce)-1], ciphertext); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("invalid nonce error=%v, want ErrCipherTampered", err)
	}

	recorder.assertZeroed(t, 32, 32, 32)
}

func TestExportAEADSubkeyClearsOnSuccessAndErrorPaths(t *testing.T) {
	recorder := installCryptoClearRecorder(t)
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:           strings.Repeat("1", 32),
		SelectionDigest:    strings.Repeat("2", 64),
		ArchiveProfile:     "zip_deflate_v1",
		FormatVersion:      1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}

	if _, err := newExportAEAD(dek, binding); err != nil {
		t.Fatalf("new Export AEAD: %v", err)
	}
	if _, err := EncryptStreamWithNonce(
		context.Background(), nil, bytes.NewReader([]byte("payload")), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("invalid encryption target error=%v, want ErrCipherTampered", err)
	}

	recorder.assertZeroed(t, 32, 32)
}

func TestUnwrapJobDEKClearsAuthenticatedWrongLengthPlaintext(t *testing.T) {
	recorder := installCryptoClearRecorder(t)
	kek := bytes.Repeat([]byte{7}, 32)
	binding := JobKeyBinding{
		ExportID:        strings.Repeat("1", 32),
		SelectionDigest: strings.Repeat("a", 64),
		KEKVersion:      1,
		WrapAlgorithm:   JobKeyWrapAlgorithmV1,
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{8}, aead.NonceSize())
	envelope := JobKeyEnvelope{
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, bytes.Repeat([]byte{9}, 31), jobKeyAD(binding)),
	}
	if dek, err := UnwrapJobDEK(binding, kek, envelope); dek != nil || !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("wrong-length unwrap dek=%x err=%v", dek, err)
	}

	recorder.assertZeroed(t, 31)
}

type cryptoClearRecorder struct {
	mu     sync.Mutex
	values [][]byte
}

func installCryptoClearRecorder(t *testing.T) *cryptoClearRecorder {
	t.Helper()
	recorder := &cryptoClearRecorder{}
	observer := cryptoClearObserver(func(value []byte) {
		recorder.mu.Lock()
		recorder.values = append(recorder.values, append([]byte(nil), value...))
		recorder.mu.Unlock()
	})
	previous := exportCryptoClearObserver.Swap(&observer)
	t.Cleanup(func() { exportCryptoClearObserver.Store(previous) })
	return recorder
}

func (recorder *cryptoClearRecorder) assertZeroed(t *testing.T, lengths ...int) {
	t.Helper()
	recorder.mu.Lock()
	values := append([][]byte(nil), recorder.values...)
	recorder.mu.Unlock()
	if len(values) != len(lengths) {
		t.Fatalf("zeroization calls=%d, want %d", len(values), len(lengths))
	}
	for index, value := range values {
		if len(value) != lengths[index] {
			t.Fatalf("zeroization call %d length=%d, want %d", index, len(value), lengths[index])
		}
		for _, byteValue := range value {
			if byteValue != 0 {
				t.Fatalf("zeroization call %d retained non-zero data", index)
			}
		}
	}
}

func TestDecryptCipherReadFailuresRemainRetryable(t *testing.T) {
	dek, binding, ciphertext, metadata := exportCryptoCipherFixture(t, []byte("retryable storage failure"))

	t.Run("stream seek", func(t *testing.T) {
		var destination bytes.Buffer
		_, err := DecryptStream(context.Background(), &destination, &faultingCipherSource{
			Reader: bytes.NewReader(ciphertext), seekErr: syscall.EIO,
		}, dek, binding)
		assertRetryableCipherReadError(t, err)
		if destination.Len() != 0 {
			t.Fatalf("stream wrote %d plaintext bytes before retryable seek failure", destination.Len())
		}
	})

	t.Run("stream", func(t *testing.T) {
		var destination bytes.Buffer
		_, err := DecryptStream(context.Background(), &destination, &faultingCipherSource{
			Reader: bytes.NewReader(ciphertext), readErr: syscall.EIO,
		}, dek, binding)
		assertRetryableCipherReadError(t, err)
		if destination.Len() != 0 {
			t.Fatalf("stream wrote %d plaintext bytes before retryable read failure", destination.Len())
		}
	})

	t.Run("range", func(t *testing.T) {
		var destination bytes.Buffer
		_, err := DecryptRange(context.Background(), &destination, &faultingCipherSource{
			Reader: bytes.NewReader(ciphertext), readAtErr: syscall.EIO,
		}, dek, binding, metadata, 0, 1)
		assertRetryableCipherReadError(t, err)
		if destination.Len() != 0 {
			t.Fatalf("range wrote %d plaintext bytes before retryable read failure", destination.Len())
		}
	})
}

func TestDecryptStreamObservesCancellationAfterFinalTrailerRead(t *testing.T) {
	dek, binding, ciphertext, _ := exportCryptoCipherFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &blockingFinalTrailerReadSeeker{
		Reader:      bytes.NewReader(ciphertext),
		blockOffset: cipherHeaderBytesV1 + cipherLengthPrefixBytesV1,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	var destination bytes.Buffer
	result := make(chan error, 1)
	go func() {
		_, err := DecryptStream(ctx, &destination, source, dek, binding)
		result <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("DecryptStream did not begin the final trailer read")
	}
	cancel()
	close(source.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("final trailer cancellation error=%v, want context.Canceled", err)
		}
		if errors.Is(err, ErrCipherTampered) {
			t.Fatalf("final trailer cancellation mapped to ErrCipherTampered: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DecryptStream did not return after final trailer cancellation")
	}
	if destination.Len() != 0 {
		t.Fatalf("canceled trailer read wrote %d plaintext bytes", destination.Len())
	}
}

func TestDecryptStreamPrefersCancellationAfterBlockingReadErrors(t *testing.T) {
	tests := []struct {
		name        string
		plaintext   []byte
		blockOffset int64
		readErr     error
	}{
		{name: "header", blockOffset: 0, readErr: io.ErrUnexpectedEOF},
		{name: "length prefix", blockOffset: cipherHeaderBytesV1, readErr: syscall.EIO},
		{name: "final trailer", blockOffset: cipherHeaderBytesV1 + cipherLengthPrefixBytesV1, readErr: io.ErrUnexpectedEOF},
		{
			name: "record", plaintext: []byte("blocked record read"),
			blockOffset: cipherHeaderBytesV1 + cipherLengthPrefixBytesV1, readErr: syscall.EIO,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			dek, binding, ciphertext, _ := exportCryptoCipherFixture(t, test.plaintext)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &blockingFinalTrailerReadSeeker{
				Reader: bytes.NewReader(ciphertext), blockOffset: test.blockOffset,
				afterReleaseErr: test.readErr, entered: make(chan struct{}), release: make(chan struct{}),
			}
			var destination bytes.Buffer
			result := make(chan error, 1)
			go func() {
				_, err := DecryptStream(ctx, &destination, source, dek, binding)
				result <- err
			}()
			select {
			case <-source.entered:
			case <-time.After(time.Second):
				t.Fatalf("DecryptStream did not begin the %s read", test.name)
			}
			cancel()
			close(source.release)
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s cancellation error=%v, want context.Canceled", test.name, err)
				}
				if errors.Is(err, ErrCipherTampered) {
					t.Fatalf("%s cancellation mapped to ErrCipherTampered: %v", test.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("DecryptStream did not return after %s cancellation", test.name)
			}
			if destination.Len() != 0 {
				t.Fatalf("%s cancellation wrote %d plaintext bytes", test.name, destination.Len())
			}
		})
	}
}

func TestDecryptStreamPrefersCancellationBeforeAndAfterSeek(t *testing.T) {
	t.Run("before initial seek", func(t *testing.T) {
		dek, binding, ciphertext, _ := exportCryptoCipherFixture(t, []byte("canceled before seek"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		source := &countingSeekCipherSource{Reader: bytes.NewReader(ciphertext)}
		_, err := DecryptStream(ctx, &bytes.Buffer{}, source, dek, binding)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("initial seek cancellation error=%v, want context.Canceled", err)
		}
		if source.calls != 0 {
			t.Fatalf("initial seek calls=%d, want 0", source.calls)
		}
	})

	t.Run("after rewind seek error", func(t *testing.T) {
		dek, binding, ciphertext, _ := exportCryptoCipherFixture(t, []byte("canceled after rewind seek"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &blockingSeekCipherSource{
			Reader: bytes.NewReader(ciphertext), blockOnSeek: 2, seekErr: syscall.EIO,
			entered: make(chan struct{}), release: make(chan struct{}),
		}
		result := make(chan error, 1)
		go func() {
			_, err := DecryptStream(ctx, &bytes.Buffer{}, source, dek, binding)
			result <- err
		}()
		select {
		case <-source.entered:
		case <-time.After(time.Second):
			t.Fatal("DecryptStream did not begin the rewind seek")
		}
		cancel()
		close(source.release)
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("rewind seek cancellation error=%v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("DecryptStream did not return after rewind seek cancellation")
		}
	})
}

func TestDecryptStreamMapsFinalTrailingUnexpectedEOFToTampered(t *testing.T) {
	dek, binding, ciphertext, _ := exportCryptoCipherFixture(t, []byte("truncated trailing stream read"))
	var destination bytes.Buffer
	_, err := DecryptStream(context.Background(), &destination, &unexpectedEOFFinalTrailerReadSeeker{
		Reader:    bytes.NewReader(ciphertext),
		endOffset: int64(len(ciphertext)),
	}, dek, binding)
	assertCipherTampered(t, err)
	if destination.Len() != 0 {
		t.Fatalf("stream wrote %d plaintext bytes before trailing truncation rejection", destination.Len())
	}
}

func TestDecryptRangeMapsUnexpectedEOFToTampered(t *testing.T) {
	dek, binding, ciphertext, metadata := exportCryptoCipherFixture(t, []byte("truncated range authentication read"))
	var destination bytes.Buffer
	_, err := DecryptRange(context.Background(), &destination, &faultingCipherSource{
		Reader: bytes.NewReader(ciphertext), readAtErr: io.ErrUnexpectedEOF,
	}, dek, binding, metadata, 0, 1)
	assertCipherTampered(t, err)
	if destination.Len() != 0 {
		t.Fatalf("range wrote %d plaintext bytes before truncation rejection", destination.Len())
	}
}

func TestReadCipherAtMapsUnexpectedEOFToTampered(t *testing.T) {
	_, err := readCipherAt(context.Background(), &faultingCipherSource{
		Reader: bytes.NewReader(nil), readAtErr: io.ErrUnexpectedEOF,
	}, 0, 1)
	assertCipherTampered(t, err)
}

func exportCryptoCipherFixture(t *testing.T, plaintext []byte) ([]byte, CipherBinding, []byte, CipherRangeMetadata) {
	t.Helper()
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:           strings.Repeat("1", 32),
		SelectionDigest:    strings.Repeat("2", 64),
		ArchiveProfile:     "zip_deflate_v1",
		FormatVersion:      1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	var encoded bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &encoded, bytes.NewReader(plaintext), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	return dek, binding, append([]byte(nil), encoded.Bytes()...), CipherRangeMetadata{
		ChunkBytes:       result.ChunkBytes,
		ChunkCount:       result.ChunkCount,
		PlaintextBytes:   result.PlaintextBytes,
		CiphertextBytes:  result.CiphertextBytes,
		PlaintextDigest:  result.PlaintextDigest,
		CiphertextDigest: result.CiphertextDigest,
		NoncePrefix:      append([]byte(nil), result.NoncePrefix...),
	}
}

func assertRetryableCipherReadError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("read failure error=%v, want syscall.EIO", err)
	}
	if errors.Is(err, ErrCipherTampered) {
		t.Fatalf("read failure mapped to ErrCipherTampered: %v", err)
	}
}

func assertCipherTampered(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("error=%v, want ErrCipherTampered", err)
	}
}

type faultingCipherSource struct {
	*bytes.Reader
	readErr   error
	readAtErr error
	seekErr   error
}

func (source *faultingCipherSource) Read(buffer []byte) (int, error) {
	if source.readErr != nil {
		return 0, source.readErr
	}
	return source.Reader.Read(buffer)
}

func (source *faultingCipherSource) ReadAt(buffer []byte, offset int64) (int, error) {
	if source.readAtErr != nil {
		return 0, source.readAtErr
	}
	return source.Reader.ReadAt(buffer, offset)
}

func (source *faultingCipherSource) Seek(offset int64, whence int) (int64, error) {
	if source.seekErr != nil {
		return 0, source.seekErr
	}
	return source.Reader.Seek(offset, whence)
}

type blockingFinalTrailerReadSeeker struct {
	*bytes.Reader
	blockOffset     int64
	afterReleaseErr error
	entered         chan struct{}
	release         chan struct{}
	once            sync.Once
}

func (source *blockingFinalTrailerReadSeeker) Read(buffer []byte) (int, error) {
	offset, err := source.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if offset == source.blockOffset {
		source.once.Do(func() {
			close(source.entered)
			<-source.release
		})
		if source.afterReleaseErr != nil {
			return 0, source.afterReleaseErr
		}
	}
	return source.Reader.Read(buffer)
}

type countingSeekCipherSource struct {
	*bytes.Reader
	calls int
}

func (source *countingSeekCipherSource) Seek(offset int64, whence int) (int64, error) {
	source.calls++
	return source.Reader.Seek(offset, whence)
}

type blockingSeekCipherSource struct {
	*bytes.Reader
	blockOnSeek int
	seekErr     error
	entered     chan struct{}
	release     chan struct{}
	calls       int
}

func (source *blockingSeekCipherSource) Seek(offset int64, whence int) (int64, error) {
	source.calls++
	if source.calls == source.blockOnSeek {
		close(source.entered)
		<-source.release
		return 0, source.seekErr
	}
	return source.Reader.Seek(offset, whence)
}

type unexpectedEOFFinalTrailerReadSeeker struct {
	*bytes.Reader
	endOffset int64
}

func (source *unexpectedEOFFinalTrailerReadSeeker) Read(buffer []byte) (int, error) {
	offset, err := source.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if offset == source.endOffset {
		return 0, io.ErrUnexpectedEOF
	}
	return source.Reader.Read(buffer)
}

func TestChunkAssociatedDataUsesExactCanonicalDomainAndRawProfile(t *testing.T) {
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeFinalArchive,
	}
	associatedData := cipherAssociatedData(binding, bytes.Repeat([]byte{9}, 8), 65536, 3, 17, false)
	wantDomain := "xirang.export.chunk.v1"
	if len(associatedData) < 8 || binary.BigEndian.Uint64(associatedData[:8]) != uint64(len(wantDomain)) ||
		!bytes.Equal(associatedData[8:8+len(wantDomain)], []byte(wantDomain)) {
		t.Fatalf("first associated-data field=%x, want length-prefixed %q", associatedData, wantDomain)
	}
	if bytes.Contains(associatedData, []byte("zip:zip_deflate_v1")) || !bytes.Contains(associatedData, []byte(binding.ArchiveProfile)) {
		t.Fatalf("associated data must bind raw persisted profile %q: %x", binding.ArchiveProfile, associatedData)
	}
}

func TestChunkAEADRoundTripAndAssociatedDataTamper(t *testing.T) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeFinalArchive,
	}
	plaintext := bytes.Repeat([]byte("chunked-export-"), 100)
	var ciphertext bytes.Buffer
	result, err := EncryptStream(context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, 128)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunkCount < 2 || result.PlaintextBytes != int64(len(plaintext)) || result.CiphertextBytes != int64(ciphertext.Len()) {
		t.Fatalf("result=%+v ciphertext=%d", result, ciphertext.Len())
	}
	var decoded bytes.Buffer
	if _, err := DecryptStream(context.Background(), &decoded, bytes.NewReader(ciphertext.Bytes()), dek, binding); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), plaintext) {
		t.Fatal("round trip plaintext mismatch")
	}

	tamperedBinding := binding
	tamperedBinding.SelectionDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := DecryptStream(context.Background(), &bytes.Buffer{}, bytes.NewReader(ciphertext.Bytes()), dek, tamperedBinding); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("AD tamper error=%v want ErrCipherTampered", err)
	}
	tamperedCiphertext := append([]byte(nil), ciphertext.Bytes()...)
	tamperedCiphertext[len(tamperedCiphertext)-1] ^= 1
	if _, err := DecryptStream(context.Background(), &bytes.Buffer{}, bytes.NewReader(tamperedCiphertext), dek, binding); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("ciphertext tamper error=%v want ErrCipherTampered", err)
	}
}

func TestDecryptStreamRejectsTrailerDamageWithoutPlaintextOutput(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:           strings.Repeat("1", 32),
		SelectionDigest:    strings.Repeat("2", 64),
		ArchiveProfile:     "zip_deflate_v1",
		FormatVersion:      1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	plaintext := bytes.Repeat([]byte("authenticated-data-chunk"), 16)
	var ciphertext bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunkCount < 2 {
		t.Fatalf("chunk count=%d, want at least two valid data chunks", result.ChunkCount)
	}

	tests := []struct {
		name       string
		ciphertext []byte
	}{
		{
			name:       "tampered trailer",
			ciphertext: append([]byte(nil), ciphertext.Bytes()...),
		},
		{
			name:       "truncated trailer",
			ciphertext: append([]byte(nil), ciphertext.Bytes()[:ciphertext.Len()-1]...),
		},
	}
	tests[0].ciphertext[len(tests[0].ciphertext)-1] ^= 1

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var destination bytes.Buffer
			if _, err := DecryptStream(
				context.Background(), &destination, bytes.NewReader(test.ciphertext), dek, binding,
			); !errors.Is(err, ErrCipherTampered) {
				t.Fatalf("decrypt error=%v, want ErrCipherTampered", err)
			}
			if destination.Len() != 0 {
				t.Fatalf("wrote %d plaintext bytes before trailer authentication", destination.Len())
			}
		})
	}
}

func TestDecryptStreamRejectsNonSeekableSourceWithoutPlaintextOutput(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:           strings.Repeat("1", 32),
		SelectionDigest:    strings.Repeat("2", 64),
		ArchiveProfile:     "zip_deflate_v1",
		FormatVersion:      1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	var ciphertext bytes.Buffer
	if _, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(bytes.Repeat([]byte("chunk"), 32)), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	); err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	if _, err := DecryptStream(
		context.Background(), &destination, io.LimitReader(bytes.NewReader(ciphertext.Bytes()), int64(ciphertext.Len())), dek, binding,
	); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("decrypt error=%v, want ErrCipherTampered", err)
	}
	if destination.Len() != 0 {
		t.Fatalf("non-seekable source wrote %d plaintext bytes", destination.Len())
	}
}

func TestCipherResultCarriesAuthenticatedHeaderChunkBytes(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:        strings.Repeat("1", 32),
		SelectionDigest: strings.Repeat("2", 64),
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	const chunkBytes = 257
	var ciphertext bytes.Buffer
	encrypted, err := EncryptStream(
		context.Background(), &ciphertext, bytes.NewReader(bytes.Repeat([]byte("x"), 1025)),
		dek, binding, chunkBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptStream(
		context.Background(), io.Discard, bytes.NewReader(ciphertext.Bytes()), dek, binding,
	)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted.ChunkBytes != chunkBytes || decrypted.ChunkBytes != chunkBytes {
		t.Fatalf("chunk bytes encrypted=%d decrypted=%d want=%d", encrypted.ChunkBytes, decrypted.ChunkBytes, chunkBytes)
	}
}

func TestCiphertextSizeV1UsesExactPhysicalLayout(t *testing.T) {
	const chunkBytes int64 = 64
	tests := []struct {
		name      string
		plaintext int64
		want      int64
	}{
		{name: "empty", plaintext: 0, want: 88},
		{name: "one_byte", plaintext: 1, want: 109},
		{name: "one_full_chunk", plaintext: chunkBytes, want: 172},
		{name: "full_plus_partial", plaintext: chunkBytes + 1, want: 193},
		{name: "three_full_chunks", plaintext: 3 * chunkBytes, want: 340},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ciphertextSizeV1(test.plaintext, chunkBytes)
			if err != nil || got != test.want {
				t.Fatalf("ciphertextSizeV1(%d, %d)=%d, %v; want %d", test.plaintext, chunkBytes, got, err, test.want)
			}
		})
	}
}

func TestCiphertextSizeV1RejectsInvalidCounterAndIntegerOverflow(t *testing.T) {
	maxChunks := int64(math.MaxUint32)
	wantAtCounterLimit := maxChunks + 20*maxChunks + 88
	got, err := ciphertextSizeV1(maxChunks, 1)
	if err != nil || got != wantAtCounterLimit {
		t.Fatalf("max counter size=%d, %v; want %d", got, err, wantAtCounterLimit)
	}

	tests := []struct {
		name       string
		plaintext  int64
		chunkBytes int64
	}{
		{name: "negative plaintext", plaintext: -1, chunkBytes: 64},
		{name: "zero chunk", plaintext: 1, chunkBytes: 0},
		{name: "negative chunk", plaintext: 1, chunkBytes: -1},
		{name: "counter overflow", plaintext: maxChunks + 1, chunkBytes: 1},
		{name: "size overflow", plaintext: math.MaxInt64, chunkBytes: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ciphertextSizeV1(test.plaintext, test.chunkBytes); !errors.Is(err, ErrArchiveLimit) {
				t.Fatalf("ciphertextSizeV1(%d, %d) error=%v; want ErrArchiveLimit", test.plaintext, test.chunkBytes, err)
			}
		})
	}
}

func TestCiphertextSizeV1RejectsOversizedV1Chunk(t *testing.T) {
	tests := []struct {
		name           string
		plaintextBytes int64
	}{
		{name: "empty", plaintextBytes: 0},
		{name: "nonempty", plaintextBytes: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ciphertextSizeV1(test.plaintextBytes, 8*1024*1024+1); !errors.Is(err, ErrArchiveLimit) {
				t.Fatalf("ciphertextSizeV1(%d, oversized chunk) error=%v, want ErrArchiveLimit", test.plaintextBytes, err)
			}
		})
	}
}

func TestCiphertextSizeV1MatchesEncryptStreamAndRangeLayout(t *testing.T) {
	const chunkBytes = 64
	plaintext := bytes.Repeat([]byte("x"), chunkBytes+1)
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:        strings.Repeat("1", 32),
		SelectionDigest: strings.Repeat("2", 64),
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	var ciphertext bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, chunkBytes, bytes.Repeat([]byte{8}, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ciphertextSizeV1(int64(len(plaintext)), chunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	metadata := CipherRangeMetadata{
		ChunkBytes: int64(chunkBytes), ChunkCount: result.ChunkCount,
		PlaintextBytes: result.PlaintextBytes, CiphertextBytes: result.CiphertextBytes,
	}
	_, layoutSize, ok := cipherRangeLayout(metadata, 16)
	if !ok || want != int64(ciphertext.Len()) || want != result.CiphertextBytes || want != layoutSize {
		t.Fatalf("helper=%d buffer=%d result=%d layout=%d ok=%v", want, ciphertext.Len(), result.CiphertextBytes, layoutSize, ok)
	}
}

func TestWriteCipherRecordRejectsTrailerCounterBeforeMutation(t *testing.T) {
	binding := CipherBinding{
		ExportID:        strings.Repeat("1", 32),
		SelectionDigest: strings.Repeat("2", 64),
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: strings.Repeat("3", 64),
		Purpose:            CipherPurposeFinalArchive,
	}
	aead, err := newExportAEAD(bytes.Repeat([]byte{7}, 32), binding)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext bytes.Buffer
	if err := writeCipherRecord(
		&ciphertext, aead, bytes.Repeat([]byte{8}, 8), 1, math.MaxUint32-1, []byte("x"), binding,
	); err != nil {
		t.Fatalf("last data counter rejected: %v", err)
	}
	before := ciphertext.Len()
	if err := writeCipherRecord(
		&ciphertext, aead, bytes.Repeat([]byte{8}, 8), 1, math.MaxUint32, []byte("x"), binding,
	); !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("trailer counter error=%v, want ErrArchiveLimit", err)
	}
	if ciphertext.Len() != before {
		t.Fatalf("rejected trailer counter mutated destination: before=%d after=%d", before, ciphertext.Len())
	}
}

func TestChunkAEADBindsPurposeFenceNonceAndAuthenticatedTrailer(t *testing.T) {
	dek := bytes.Repeat([]byte{3}, 32)
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeItemSpool,
		ObjectID:           "22222222222222222222222222222222",
	}
	noncePrefix := bytes.Repeat([]byte{9}, 8)
	plaintext := bytes.Repeat([]byte("authenticated-spool"), 32)
	var ciphertext bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, 64, noncePrefix,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.NoncePrefix, noncePrefix) || result.ArchiveDigest != result.PlaintextDigest || result.ChunkCount < 2 {
		t.Fatalf("cipher result=%+v", result)
	}
	var decoded bytes.Buffer
	got, err := DecryptStream(context.Background(), &decoded, bytes.NewReader(ciphertext.Bytes()), dek, binding)
	if err != nil || !bytes.Equal(decoded.Bytes(), plaintext) || got.ArchiveDigest != result.ArchiveDigest {
		t.Fatalf("decrypt=%+v err=%v plaintext_match=%v", got, err, bytes.Equal(decoded.Bytes(), plaintext))
	}

	wrongPurpose := binding
	wrongPurpose.Purpose = CipherPurposeFinalArchive
	wrongPurpose.ObjectID = ""
	if _, err := DecryptStream(context.Background(), &bytes.Buffer{}, bytes.NewReader(ciphertext.Bytes()), dek, wrongPurpose); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("cross-purpose error=%v", err)
	}
	wrongFence := binding
	wrongFence.AttemptFenceDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := DecryptStream(context.Background(), &bytes.Buffer{}, bytes.NewReader(ciphertext.Bytes()), dek, wrongFence); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("cross-fence error=%v", err)
	}
	truncated := ciphertext.Bytes()[:ciphertext.Len()-1]
	if _, err := DecryptStream(context.Background(), &bytes.Buffer{}, bytes.NewReader(truncated), dek, binding); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("truncated trailer error=%v", err)
	}
}

func TestJobDEKEnvelopeIsExportAndVersionBound(t *testing.T) {
	kek := bytes.Repeat([]byte{7}, 32)
	dek := bytes.Repeat([]byte{9}, 32)
	binding := JobKeyBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		KEKVersion:      3,
		WrapAlgorithm:   JobKeyWrapAlgorithmV1,
	}
	envelope, err := WrapJobDEK(binding, kek, dek)
	if err != nil {
		t.Fatal(err)
	}
	unwrapped, err := UnwrapJobDEK(binding, kek, envelope)
	if err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("unwrap=%x err=%v", unwrapped, err)
	}
	mutations := map[string]func(*JobKeyBinding){
		"export_id": func(value *JobKeyBinding) {
			value.ExportID = "22222222222222222222222222222222"
		},
		"selection_digest": func(value *JobKeyBinding) {
			value.SelectionDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"kek_version": func(value *JobKeyBinding) {
			value.KEKVersion++
		},
		"wrap_algorithm": func(value *JobKeyBinding) {
			value.WrapAlgorithm = "aes-256-gcm-v2"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tampered := binding
			mutate(&tampered)
			if _, err := UnwrapJobDEK(tampered, kek, envelope); !errors.Is(err, ErrCipherTampered) {
				t.Fatalf("binding tamper error=%v, want ErrCipherTampered", err)
			}
		})
	}
}

func TestSelectionMetadataUsesBoundHKDFSubkeyInsteadOfRootDEK(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	jobID := "11111111111111111111111111111111"
	itemID := "22222222222222222222222222222222"
	selectionDigest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	components := []string{"root", "report.txt"}

	nonce, ciphertext, err := encryptSelectionPath(dek, jobID, itemID, selectionDigest, components)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		t.Fatal(err)
	}
	rootAEAD, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootAEAD.Open(nil, nonce, ciphertext, selectionMetadataADForTest(jobID, itemID, selectionDigest)); err == nil {
		t.Fatal("root job DEK opened selection metadata; want a domain-separated subkey")
	}

	v1Key := selectionMetadataSubkeyForTest(t, dek, "selection_metadata.v1", jobID, selectionDigest)
	v1Block, err := aes.NewCipher(v1Key)
	if err != nil {
		t.Fatal(err)
	}
	v1AEAD, err := cipher.NewGCM(v1Block)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v1AEAD.Open(nil, nonce, ciphertext, selectionMetadataADForTest(jobID, itemID, selectionDigest)); err != nil {
		t.Fatalf("selection_metadata.v1 subkey cannot authenticate metadata: %v", err)
	}
	decoded, err := decryptSelectionPath(dek, jobID, itemID, selectionDigest, nonce, ciphertext)
	if err != nil || len(decoded) != len(components) || decoded[0] != components[0] || decoded[1] != components[1] {
		t.Fatalf("round trip=%v err=%v", decoded, err)
	}

	bindings := []struct {
		name      string
		jobID     string
		itemID    string
		selection string
	}{
		{name: "job", jobID: "33333333333333333333333333333333", itemID: itemID, selection: selectionDigest},
		{name: "item", jobID: jobID, itemID: "44444444444444444444444444444444", selection: selectionDigest},
		{name: "selection", jobID: jobID, itemID: itemID, selection: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	for _, binding := range bindings {
		t.Run(binding.name, func(t *testing.T) {
			if _, err := decryptSelectionPath(dek, binding.jobID, binding.itemID, binding.selection, nonce, ciphertext); !errors.Is(err, ErrCipherTampered) {
				t.Fatalf("binding drift error=%v, want ErrCipherTampered", err)
			}
		})
	}

	wrongDomainKey := selectionMetadataSubkeyForTest(t, dek, "selection_metadata.v2", jobID, selectionDigest)
	wrongDomainBlock, err := aes.NewCipher(wrongDomainKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomainAEAD, err := cipher.NewGCM(wrongDomainBlock)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomainCiphertext := wrongDomainAEAD.Seal(nil, nonce, []byte(`["root","report.txt"]`), selectionMetadataADForTest(jobID, itemID, selectionDigest))
	if _, err := decryptSelectionPath(dek, jobID, itemID, selectionDigest, nonce, wrongDomainCiphertext); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("wrong HKDF domain error=%v, want ErrCipherTampered", err)
	}
}

func selectionMetadataSubkeyForTest(t *testing.T, dek []byte, domain, jobID, selectionDigest string) []byte {
	t.Helper()
	var info bytes.Buffer
	writeString(&info, domain)
	writeString(&info, jobID)
	writeString(&info, selectionDigest)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, dek, nil, info.Bytes()), key); err != nil {
		t.Fatal(err)
	}
	return key
}

func selectionMetadataADForTest(jobID, itemID, selectionDigest string) []byte {
	return []byte("xirang.backup_asset.export.selection_path.v1\x00" + jobID + "\x00" + itemID + "\x00" + selectionDigest)
}

func TestDecryptRangeAuthenticatesArtifactAndSelectedChunksBeforeWrite(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeFinalArchive,
	}
	plaintext := bytes.Repeat([]byte("0123456789abcdef"), 20)
	var ciphertext bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, 64, bytes.Repeat([]byte{8}, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := CipherRangeMetadata{
		ChunkBytes: 64, ChunkCount: result.ChunkCount,
		PlaintextBytes: result.PlaintextBytes, CiphertextBytes: result.CiphertextBytes,
		PlaintextDigest: result.PlaintextDigest, CiphertextDigest: result.CiphertextDigest,
		NoncePrefix: result.NoncePrefix,
	}
	offset, length := int64(59), int64(80)
	var decoded bytes.Buffer
	read, err := DecryptRange(
		context.Background(), &decoded, bytes.NewReader(ciphertext.Bytes()), dek, binding, metadata, offset, length,
	)
	if err != nil {
		t.Fatalf("decrypt range: %v", err)
	}
	if !bytes.Equal(decoded.Bytes(), plaintext[offset:offset+length]) || read.PlaintextBytes != length ||
		read.CiphertextBytes < metadata.CiphertextBytes {
		t.Fatalf("range result=%+v bytes=%q", read, decoded.Bytes())
	}

	tampered := append([]byte(nil), ciphertext.Bytes()...)
	tampered[len(tampered)-1] ^= 1 // outside the selected chunk records: whole-artifact digest must still close delivery.
	decoded.Reset()
	if _, err := DecryptRange(
		context.Background(), &decoded, bytes.NewReader(tampered), dek, binding, metadata, offset, length,
	); !errors.Is(err, ErrCipherTampered) {
		t.Fatalf("tampered range error=%v", err)
	}
	if decoded.Len() != 0 {
		t.Fatalf("tampered artifact wrote %d unauthenticated bytes", decoded.Len())
	}
}

func TestDecryptRangeRejectsEveryTruncatedObjectWithoutOutput(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	binding := CipherBinding{
		ExportID:        "11111111111111111111111111111111",
		SelectionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArchiveProfile:  "zip_deflate_v1", FormatVersion: 1,
		AttemptFenceDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Purpose:            CipherPurposeFinalArchive,
	}
	plaintext := bytes.Repeat([]byte("range-truncation"), 16)
	var ciphertext bytes.Buffer
	result, err := EncryptStreamWithNonce(
		context.Background(), &ciphertext, bytes.NewReader(plaintext), dek, binding, 64, bytes.Repeat([]byte{9}, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := CipherRangeMetadata{
		ChunkBytes: 64, ChunkCount: result.ChunkCount, PlaintextBytes: result.PlaintextBytes,
		CiphertextBytes: result.CiphertextBytes, PlaintextDigest: result.PlaintextDigest,
		CiphertextDigest: result.CiphertextDigest, NoncePrefix: result.NoncePrefix,
	}
	for length := 0; length < ciphertext.Len(); length++ {
		var output bytes.Buffer
		_, err := DecryptRange(
			context.Background(), &output, bytes.NewReader(ciphertext.Bytes()[:length]), dek, binding, metadata, 0, 1,
		)
		if !errors.Is(err, ErrCipherTampered) {
			t.Fatalf("truncated length=%d err=%v", length, err)
		}
		if output.Len() != 0 {
			t.Fatalf("truncated length=%d wrote %d bytes", length, output.Len())
		}
	}
}

func TestCipherRangeLayoutRejectsIntegerOverflow(t *testing.T) {
	digest := sha256.Sum256([]byte("overflow-fixture"))
	metadata := CipherRangeMetadata{
		ChunkBytes: 1, ChunkCount: 1, PlaintextBytes: 1, CiphertextBytes: math.MaxInt64,
		PlaintextDigest: hex.EncodeToString(digest[:]), CiphertextDigest: hex.EncodeToString(digest[:]),
		NoncePrefix: bytes.Repeat([]byte{1}, 8),
	}
	if _, _, ok := cipherRangeLayout(metadata, math.MaxInt); ok {
		t.Fatal("layout accepted overflowing AEAD overhead")
	}
	if _, _, ok := cipherRangeChunkLayout(metadata, math.MaxInt, 0); ok {
		t.Fatal("chunk layout accepted overflowing AEAD overhead")
	}
}
