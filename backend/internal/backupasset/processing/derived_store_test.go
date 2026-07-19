package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

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
	if err := db.AutoMigrate(&model.WrappedDomainKey{}, &model.BackupAssetDerivedBlob{}); err != nil {
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
