package updater

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestStreamingStoreCancellationAfterSourceStabilityRemovesStaging(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	verified := verifiedBundleForStore(t, []BundleFilePayload{{
		Path: "models/model.dat", Mode: 0o444, Content: bytes.Repeat([]byte("x"), 128<<10),
	}})
	bundle, _, err := BuildCanonicalTar(verified.Files)
	if err != nil {
		t.Fatal(err)
	}
	verified.Files = nil
	ctx, cancel := context.WithCancel(context.Background())
	_, err = store.storeVerifiedBundle(ctx, verified, bytes.NewReader(bundle), func(context.Context) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation error=%v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "bundles"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("canceled store published or left staging: entries=%v err=%v", entries, readErr)
	}
}

func TestStoreBundleAtomicallyPublishesImmutableContentAddressedTree(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	verified := verifiedBundleForStore(t, []BundleFilePayload{
		{Path: "models/ocr.dat", Mode: 0o444, Content: []byte("model")},
		{Path: "policies/parser.json", Mode: 0o444, Content: []byte(`{"schema_version":1}`)},
	})
	stored, err := store.StoreBundle(context.Background(), verified)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BundleFingerprint != verified.BundleFingerprint || stored.AlreadyPresent {
		t.Fatalf("unexpected stored receipt: %+v", stored)
	}
	bundleRoot := filepath.Join(root, "bundles", verified.BundleFingerprint)
	receiptPath := filepath.Join(bundleRoot, storedBundleReceiptPath)
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode().Perm() != 0o444 {
		t.Fatalf("stored bundle receipt is missing or mutable: info=%v err=%v", receiptInfo, err)
	}
	receiptPayload, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeStoredBundleReceipt(receiptPayload)
	if err != nil || receipt.BundleFingerprint != verified.BundleFingerprint || len(receipt.Files) != len(verified.Files) {
		t.Fatalf("stored bundle receipt=%+v err=%v", receipt, err)
	}
	for _, file := range verified.Files {
		path := filepath.Join(bundleRoot, filepath.FromSlash(file.Path))
		content, err := os.ReadFile(path)
		if err != nil || string(content) != string(file.Content) {
			t.Fatalf("stored file %s content=%q err=%v", file.Path, content, err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("stored file %s is not immutable: info=%v err=%v", file.Path, info, err)
		}
	}
	for _, directory := range []string{"models", "policies"} {
		info, statErr := os.Stat(filepath.Join(bundleRoot, directory))
		if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o555 {
			t.Fatalf("stored directory %s is not immutable: info=%v err=%v", directory, info, statErr)
		}
	}
	again, err := store.StoreBundle(context.Background(), verified)
	if err != nil || !again.AlreadyPresent {
		t.Fatalf("idempotent store=%+v err=%v", again, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "bundles"))
	if err != nil || len(entries) != 1 || entries[0].Name() != verified.BundleFingerprint {
		t.Fatalf("staging residue or extra bundle: %v err=%v", entries, err)
	}
}

func TestStoreRootReplacementCannotRedirectDescriptorAnchoredWrite(t *testing.T) {
	base := t.TempDir()
	t.Cleanup(func() { makeTreeWritable(base) })
	root := filepath.Join(base, "store")
	if err := os.Mkdir(root, sharedStoreRootMode); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(base, "store-original")
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	attacker := filepath.Join(base, "attacker")
	if err := os.MkdirAll(filepath.Join(attacker, "bundles"), sharedStoreRootMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, root); err != nil {
		t.Fatal(err)
	}
	verified := verifiedBundleForStore(t, []BundleFilePayload{{
		Path: "models/model.dat", Mode: 0o444, Content: []byte("model"),
	}})
	if _, err := store.StoreBundle(context.Background(), verified); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("replaced store root error=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(attacker, "bundles"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("replaced root redirected bundle write: entries=%v error=%v", entries, err)
	}
}

func TestStoreSourceUsesOnlyDescriptorAnchoredFilesystemMutation(t *testing.T) {
	payload, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"os.Lstat(", "os.MkdirTemp(", "os.MkdirAll(", "os.OpenFile(", "os.Rename(",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("store source retains path-based mutation primitive %q", forbidden)
		}
	}
}

func TestStoreBundleRejectsCorruptExistingTreeAndTraversal(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	verified := verifiedBundleForStore(t, []BundleFilePayload{{Path: "model.dat", Mode: 0o444, Content: []byte("model")}})
	if _, err := store.StoreBundle(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
	storedPath := filepath.Join(root, "bundles", verified.BundleFingerprint, "model.dat")
	if err := os.Chmod(storedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storedPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreBundle(context.Background(), verified); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("corrupt existing tree error=%v", err)
	}
	traversal := VerifiedBundle{
		BundleFingerprint: strings.Repeat("c", 64),
		Files:             []BundleFilePayload{{Path: "../outside", Mode: 0o444, Content: []byte("escape")}},
	}
	if _, err := store.StoreBundle(context.Background(), traversal); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("traversal error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("traversal wrote outside store: %v", err)
	}
}

func TestStoreAndActivatorRequireUpdaterOwnedGroupReadableRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore with updater-owned shared root: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
	for _, path := range []string{root, filepath.Join(root, "bundles")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o750 {
			t.Fatalf("shared store path %s mode=%v err=%v", path, info, err)
		}
	}
	if _, err := NewActivator(root); err != nil {
		t.Fatalf("NewActivator with updater-owned shared root: %v", err)
	}

	unsafeRoot := t.TempDir()
	if err := os.Chmod(unsafeRoot, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(unsafeRoot); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("group-writable store root error=%v", err)
	}
}

func newStoreTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	return root
}

func verifiedBundleForStore(t *testing.T, files []BundleFilePayload) VerifiedBundle {
	t.Helper()
	ordered := append([]BundleFilePayload(nil), files...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Path < ordered[right].Path })
	bundle, manifestFiles, err := BuildCanonicalTar(ordered)
	if err != nil {
		t.Fatalf("build canonical test bundle: %v", err)
	}
	manifest := Manifest{
		SchemaVersion: 1,
		SourceKind:    "builtin",
		SourceID:      "store-test",
		Version:       "1.0.0",
		CreatedAt:     time.Unix(0, 0).UTC(),
		ExpiresAt:     time.Unix(3600, 0).UTC(),
		Capabilities: []ManifestCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "test-tool-v1", ModelRevision: "test-model-v1", DataRevision: "none",
		}},
		Files:              manifestFiles,
		BundleSHA256:       SHA256Hex(bundle),
		SigningKeyID:       "store-test-key",
		SignatureAlgorithm: "ed25519",
	}
	fingerprint, err := bundleFingerprint(manifest)
	if err != nil {
		t.Fatalf("derive canonical test bundle fingerprint: %v", err)
	}
	return VerifiedBundle{Manifest: manifest, BundleFingerprint: fingerprint, Files: ordered}
}
