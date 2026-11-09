package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"golang.org/x/sys/unix"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDerivedStoreWritesOnlyOpaqueAuthenticatedCiphertextWithRandomDEKs(t *testing.T) {
	harness := newDerivedStoreHarness(t)
	firstPayload := bytes.Repeat([]byte("first-private-artifact"), 5000)
	secondPayload := bytes.Repeat([]byte("second-private-artifact"), 5000)
	first, err := harness.store.PutBlob(context.Background(), derivedDeclaration(firstPayload), bytes.NewReader(firstPayload))
	if err != nil {
		t.Fatalf("PutBlob(first): %v", err)
	}
	second, err := harness.store.PutBlob(context.Background(), derivedDeclaration(secondPayload), bytes.NewReader(secondPayload))
	if err != nil {
		t.Fatalf("PutBlob(second): %v", err)
	}
	if first.BlobID == second.BlobID || strings.ContainsAny(first.OpaqueLocator, `/\`) || strings.ContainsAny(second.OpaqueLocator, `/\`) {
		t.Fatalf("blob identities/locators are unsafe: first=%+v second=%+v", first, second)
	}
	var rows []model.BackupAssetDerivedBlob
	if err := harness.db.Order("id ASC").Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("load blob rows: count=%d err=%v", len(rows), err)
	}
	if bytes.Equal(rows[0].WrappedDEK, rows[1].WrappedDEK) {
		t.Fatal("different physical blobs reused one DEK envelope")
	}
	for _, row := range rows {
		path := filepath.Join(harness.root, row.OpaqueLocator)
		ciphertext, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(ciphertext, []byte("private-artifact")) {
			t.Fatal("plaintext appeared in Derived Store ciphertext")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 || row.State != "active" || row.RefCount != 0 {
			t.Fatalf("unsafe blob file/row: info=%v row=%+v err=%v", info, row, err)
		}
	}
	var plaintext bytes.Buffer
	if err := harness.store.readBlob(context.Background(), first.BlobID, &plaintext); err != nil {
		t.Fatalf("readBlob: %v", err)
	}
	if !bytes.Equal(plaintext.Bytes(), firstPayload) {
		t.Fatal("decrypted artifact differs")
	}
}

func TestDerivedStoreDeduplicatesExactPhysicalBlob(t *testing.T) {
	harness := newDerivedStoreHarness(t)
	payload := bytes.Repeat([]byte("shared"), 12000)
	first, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if first.BlobID != second.BlobID || !second.Reused {
		t.Fatalf("exact blob was not reused: first=%+v second=%+v", first, second)
	}
	entries, err := os.ReadDir(harness.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dedup root entries=%d err=%v", len(entries), err)
	}
}

func TestDerivedStoreReuseValidatesPlaintextAndReactivates(t *testing.T) {
	h := newDerivedStoreHarness(t)
	ctx := context.Background()
	payload := []byte("shared output bytes")
	declaration := derivedDeclaration(payload)
	first, err := h.store.PutBlob(ctx, declaration, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{payload[:len(payload)-1], append(append([]byte(nil), payload...), 'x'), bytes.Repeat([]byte{'x'}, len(payload))} {
		if _, err := h.store.PutBlob(ctx, declaration, bytes.NewReader(invalid)); !errors.Is(err, ErrDerivedInvalid) {
			t.Fatalf("invalid reused plaintext accepted: %v", err)
		}
	}
	if err := h.db.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", first.BlobID).Updates(map[string]any{"state": "unavailable", "wrapped_dek": []byte{}}).Error; err != nil {
		t.Fatal(err)
	}
	second, err := h.store.PutBlob(ctx, declaration, bytes.NewReader(payload))
	if err != nil || second.BlobID != first.BlobID || second.Reused || second.OpaqueLocator == first.OpaqueLocator {
		t.Fatalf("reactivated blob=%+v err=%v", second, err)
	}
	var output bytes.Buffer
	if err := h.store.readBlob(ctx, second.BlobID, &output); err != nil || !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("reactivated read=%q err=%v", output.Bytes(), err)
	}
}

func TestDerivedStoreDetectsCiphertextTamper(t *testing.T) {
	harness := newDerivedStoreHarness(t)
	payload := bytes.Repeat([]byte("tamper"), 12000)
	handle, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(harness.root, handle.OpaqueLocator)
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)/2] ^= 0x40
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := harness.store.readBlob(context.Background(), handle.BlobID, &bytes.Buffer{}); !errors.Is(err, ErrDerivedTamper) {
		t.Fatalf("tampered blob got %v", err)
	}
}

func TestDerivedStoreRewrapBatchLeavesCiphertextUnchanged(t *testing.T) {
	harness := newDerivedStoreHarness(t)
	payload := bytes.Repeat([]byte("rewrap-store"), 10000)
	handle, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(harness.root, handle.OpaqueLocator)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := harness.keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := harness.keyring.Rotate(context.Background(), backupasset.KeyDomainDerivedStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	count, err := harness.store.RewrapBatch(context.Background(), oldKey.Version, newKey.Version, 10)
	if err != nil || count != 1 {
		t.Fatalf("RewrapBatch count=%d err=%v", count, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("store rewrap changed ciphertext bytes")
	}
	var row model.BackupAssetDerivedBlob
	if err := harness.db.First(&row, "id = ?", handle.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if row.DerivedKEKVersion != newKey.Version {
		t.Fatalf("blob KEK version=%d want %d", row.DerivedKEKVersion, newKey.Version)
	}
	var plaintext bytes.Buffer
	if err := harness.store.readBlob(context.Background(), handle.BlobID, &plaintext); err != nil || !bytes.Equal(plaintext.Bytes(), payload) {
		t.Fatalf("read rewrapped blob bytes=%d err=%v", plaintext.Len(), err)
	}
}

func TestDerivedDiscardLocksBlobBeforeCountingReferences(t *testing.T) {
	harness := newDerivedStoreHarness(t)
	if err := harness.db.AutoMigrate(&model.BackupAssetDerivedBlobReference{}); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("discard-lock-order"), 3000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	queries := make([]string, 0, 2)
	callbackName := "test:derived-discard-lock-order"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "backup_asset_derived_blobs", "backup_asset_derived_blob_references":
			mu.Lock()
			queries = append(queries, tx.Statement.Table)
			mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })
	if err := harness.store.discardBlobIfUnreferenced(context.Background(), blob.BlobID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) < 2 || queries[0] != "backup_asset_derived_blobs" || queries[1] != "backup_asset_derived_blob_references" {
		t.Fatalf("discard query order=%v, want blob lock before reference count", queries)
	}
}

type derivedStoreHarness struct {
	db      *gorm.DB
	keyring *backupasset.Keyring
	store   *DerivedStore
	root    string
}

func newDerivedStoreHarness(t *testing.T) *derivedStoreHarness {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DERIVED_STORE_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	dsn := processingTestSQLiteDSN(t)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.WrappedDomainKey{}, &model.BackupAssetDerivedBlob{}, &model.BackupAssetDerivedBlobReference{}, &model.BackupAssetProcessingUpload{}, &model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{}, &model.RecoveryPointLease{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_derived_test_key_version ON wrapped_domain_keys(domain, version)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_derived_test_key_active ON wrapped_domain_keys(domain) WHERE state = 'active'`).Error; err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return time.Date(2026, 7, 19, 7, 8, 9, 0, time.UTC) })
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "derived")
	random := &lockedSequenceReader{}
	store, err := NewDerivedStore(context.Background(), db, keyring, DerivedStoreConfig{
		Root: root, ChunkSize: 64 * 1024, BlobMaxBytes: 4 * 1024 * 1024,
		GlobalMaxBytes: 16 * 1024 * 1024, Random: random,
		ValidateRoot: func(context.Context, string) error { return nil },
	}, func() time.Time { return time.Date(2026, 7, 19, 7, 8, 9, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return &derivedStoreHarness{db: db, keyring: keyring, store: store, root: root}
}

func derivedDeclaration(payload []byte) DerivedBlobDeclaration {
	digest := sha256.Sum256(payload)
	return DerivedBlobDeclaration{PlaintextSize: int64(len(payload)), PlaintextDigest: hex.EncodeToString(digest[:])}
}

type lockedSequenceReader struct {
	mu   sync.Mutex
	next byte
}

func (reader *lockedSequenceReader) Read(payload []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for index := range payload {
		reader.next++
		payload[index] = reader.next
	}
	return len(payload), nil
}

type nonEOFReader struct{ *bytes.Reader }

func (reader nonEOFReader) Read(p []byte) (int, error) {
	if reader.Len() == 0 {
		return 0, nil
	}
	return reader.Reader.Read(p)
}

func TestDerivedStoreReuseRequiresActualEOF(t *testing.T) {
	h := newDerivedStoreHarness(t)
	payload := []byte("exact eof")
	declaration := derivedDeclaration(payload)
	if _, err := h.store.PutBlob(context.Background(), declaration, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutBlob(context.Background(), declaration, nonEOFReader{bytes.NewReader(payload)}); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatalf("missing EOF accepted: %v", err)
	}
}

func TestDerivedReplaySurvivesWinnerRevocationAndRebinds(t *testing.T) {
	h := newDerivedStoreHarness(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("replay-rebind"), 10000)
	declaration := derivedDeclaration(payload)
	first, err := h.store.newReplay(ctx, declaration, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := h.store.newReplay(ctx, declaration, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.spec.BlobID == second.spec.BlobID {
		t.Fatal("distinct contenders unexpectedly have same identity")
	}
	initial, err := h.store.putReplay(ctx, declaration, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.discardBlobIfUnreferenced(ctx, initial.BlobID); err != nil {
		t.Fatal(err)
	}
	reactivated, err := h.store.putReplay(ctx, declaration, second)
	if err != nil || reactivated.BlobID != initial.BlobID || reactivated.Reused {
		t.Fatalf("reactivation=%+v err=%v", reactivated, err)
	}
	var out bytes.Buffer
	if err := h.store.readBlob(ctx, reactivated.BlobID, &out); err != nil || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("read after rebind: %v", err)
	}
}

func TestDiscardObservedLocatorDoesNotDestroyNewIncarnation(t *testing.T) {
	h := newDerivedStoreHarness(t)
	ctx := context.Background()
	payload := []byte("incarnation")
	first, err := h.store.PutBlob(ctx, derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.discardBlobIfUnreferenced(ctx, first.BlobID); err != nil {
		t.Fatal(err)
	}
	second, err := h.store.PutBlob(ctx, derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.discardBlobIfUnreferencedObserved(ctx, second.BlobID, first.OpaqueLocator); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := h.store.readBlob(ctx, second.BlobID, &out); err != nil || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("new incarnation destroyed: %v", err)
	}
}

func TestDerivedReplayReinstallationPreservesInstalledInode(t *testing.T) {
	h := newDerivedStoreHarness(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("same-upload-reinstallation"), 6000)
	declaration := derivedDeclaration(payload)
	replay, err := h.store.newReplay(ctx, declaration, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	first, err := h.store.putReplay(ctx, declaration, replay)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(h.root, first.OpaqueLocator)
	oldFD, err := os.Open(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	defer oldFD.Close()
	retained := filepath.Join(h.root, "retained-test-link")
	if err := os.Link(oldPath, retained); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(retained)
	if err != nil {
		t.Fatal(err)
	}
	oldSpec, oldMetadata := replay.spec, cloneDerivedMetadata(replay.metadata)
	envelope, err := newDerivedAEAD(oldSpec.KEK)
	if err != nil {
		t.Fatal(err)
	}
	oldDEK, err := envelope.Open(nil, oldMetadata.EnvelopeNonce, oldMetadata.WrappedDEK, derivedEnvelopeAAD(oldSpec))
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytesLocal(oldDEK)
	if err := h.store.discardBlobIfUnreferenced(ctx, first.BlobID); err != nil {
		t.Fatal(err)
	}
	second, err := h.store.putReplay(ctx, declaration, replay)
	if err != nil || second.BlobID != first.BlobID || second.OpaqueLocator == first.OpaqueLocator {
		t.Fatalf("second install=%+v err=%v", second, err)
	}
	newDEK, err := envelope.Open(nil, replay.metadata.EnvelopeNonce, replay.metadata.WrappedDEK, derivedEnvelopeAAD(replay.spec))
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytesLocal(newDEK)
	if bytes.Equal(oldDEK, newDEK) || bytes.Equal(oldMetadata.NoncePrefix, replay.metadata.NoncePrefix) || bytes.Equal(oldMetadata.EnvelopeNonce, replay.metadata.EnvelopeNonce) {
		t.Fatal("reinstallation reused cryptographic material")
	}
	retainedBytes, err := os.ReadFile(retained)
	if err != nil || !bytes.Equal(before, retainedBytes) {
		t.Fatalf("old hardlink changed: %v", err)
	}
	fdBytes, err := io.ReadAll(oldFD)
	if err != nil || !bytes.Equal(before, fdBytes) {
		t.Fatalf("old open file changed: %v", err)
	}
	var output bytes.Buffer
	if err := h.store.readBlob(ctx, second.BlobID, &output); err != nil || !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("new generation read: %v", err)
	}
}

func TestDerivedReplayRebindFailurePreservesCandidate(t *testing.T) {
	for _, scenario := range []string{"cancel", "cancel-midstream", "random", "truncated", "trailing", "digest", "later-chunk"} {
		t.Run(scenario, func(t *testing.T) {
			h := newDerivedStoreHarness(t)
			ctx := context.Background()
			payload := bytes.Repeat([]byte("rebind-failure"), 6000)
			replay, err := h.store.newReplay(ctx, derivedDeclaration(payload), bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close()
			replay.installed = true
			switch scenario {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "cancel-midstream":
				ctx = &replayCancelContext{Context: ctx}
			case "random":
				h.store.cipher.random = replayErrorReader{}
			case "truncated":
				if err := replay.file.Truncate(replay.metadata.PhysicalSize - 1); err != nil {
					t.Fatal(err)
				}
			case "trailing":
				if _, err := replay.file.WriteAt([]byte{1}, replay.metadata.PhysicalSize); err != nil {
					t.Fatal(err)
				}
			case "digest":
				replay.spec.PlaintextDigest = strings.Repeat("0", 64)
			case "later-chunk":
				var value [1]byte
				offset := replay.metadata.PhysicalSize - 1
				if _, err := replay.file.ReadAt(value[:], offset); err != nil {
					t.Fatal(err)
				}
				value[0] ^= 1
				if _, err := replay.file.WriteAt(value[:], offset); err != nil {
					t.Fatal(err)
				}
			}
			oldFile, oldSpec, oldMetadata := replay.file, replay.spec, cloneDerivedMetadata(replay.metadata)
			before, err := os.ReadFile(oldFile.Name())
			if err != nil {
				t.Fatal(err)
			}
			if err := replay.rebind(ctx, replay.spec.BlobID); err == nil {
				t.Fatal("invalid rebind succeeded")
			}
			after, err := os.ReadFile(oldFile.Name())
			if err != nil || replay.file != oldFile || !reflect.DeepEqual(replay.spec, oldSpec) || !reflect.DeepEqual(replay.metadata, oldMetadata) || !replay.installed || !bytes.Equal(before, after) {
				t.Fatalf("old tuple or inode changed: %v", err)
			}
			entries, err := os.ReadDir(h.root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("replacement temporary file leaked: entries=%d err=%v", len(entries), err)
			}
		})
	}
}

type replayErrorReader struct{}

func (replayErrorReader) Read([]byte) (int, error) { return 0, errors.New("replay random failure") }

type replayCancelContext struct {
	context.Context
	checks int
}

func (ctx *replayCancelContext) Err() error {
	ctx.checks++
	if ctx.checks >= 3 {
		return context.Canceled
	}
	return nil
}

func TestDerivedReplayDestinationWriteFailure(t *testing.T) {
	const childEnv = "XIRANG_REPLAY_FSIZE_TEST_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDerivedReplayDestinationWriteFailure$")
		command.Env = append(os.Environ(), childEnv+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("write failure subprocess: %v\n%s", err, output)
		}
		return
	}
	h := newDerivedStoreHarness(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("write-failure-input"), 8000)
	replay, err := h.store.newReplay(ctx, derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	replay.installed = true
	oldFile, oldSpec, oldMetadata := replay.file, replay.spec, cloneDerivedMetadata(replay.metadata)
	link := filepath.Join(h.root, "retained-test-link")
	if err := os.Link(oldFile.Name(), link); err != nil {
		t.Fatal(err)
	}
	oldFD, err := os.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	defer oldFD.Close()
	before, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = uint64(len(derivedCipherMagic)) + 4 + uint64(oldSpec.ChunkSize) + 16
	if original.Cur < limited.Cur {
		t.Fatal("file size limit too low for test setup")
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	rebindErr := replay.rebind(ctx, oldSpec.BlobID)
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	signal.Reset(syscall.SIGXFSZ)
	if !errors.Is(rebindErr, syscall.EFBIG) {
		t.Fatalf("expected actual file write failure, got %v", rebindErr)
	}
	after, err := os.ReadFile(link)
	if err != nil || !bytes.Equal(before, after) || replay.file != oldFile || !reflect.DeepEqual(replay.spec, oldSpec) || !reflect.DeepEqual(replay.metadata, oldMetadata) || !replay.installed {
		t.Fatalf("write failure changed old tuple or link: %v", err)
	}
	fdBytes, err := io.ReadAll(oldFD)
	if err != nil || !bytes.Equal(before, fdBytes) {
		t.Fatalf("write failure changed old open file: %v", err)
	}
	entries, err := os.ReadDir(h.root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed candidate leaked: entries=%d err=%v", len(entries), err)
	}
}
