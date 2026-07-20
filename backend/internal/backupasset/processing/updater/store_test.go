package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreBundleAtomicallyPublishesImmutableContentAddressedTree(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	verified := VerifiedBundle{
		BundleFingerprint: strings.Repeat("a", 64),
		Files: []BundleFilePayload{
			{Path: "models/ocr.dat", Mode: 0o444, Content: []byte("model")},
			{Path: "policies/parser.json", Mode: 0o444, Content: []byte(`{"schema_version":1}`)},
		},
	}
	stored, err := store.StoreBundle(context.Background(), verified)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BundleFingerprint != verified.BundleFingerprint || stored.AlreadyPresent {
		t.Fatalf("unexpected stored receipt: %+v", stored)
	}
	bundleRoot := filepath.Join(root, "bundles", verified.BundleFingerprint)
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
	again, err := store.StoreBundle(context.Background(), verified)
	if err != nil || !again.AlreadyPresent {
		t.Fatalf("idempotent store=%+v err=%v", again, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "bundles"))
	if err != nil || len(entries) != 1 || entries[0].Name() != verified.BundleFingerprint {
		t.Fatalf("staging residue or extra bundle: %v err=%v", entries, err)
	}
}

func TestStoreBundleRejectsCorruptExistingTreeAndTraversal(t *testing.T) {
	root := newStoreTestRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	verified := VerifiedBundle{
		BundleFingerprint: strings.Repeat("b", 64),
		Files:             []BundleFilePayload{{Path: "model.dat", Mode: 0o444, Content: []byte("model")}},
	}
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
