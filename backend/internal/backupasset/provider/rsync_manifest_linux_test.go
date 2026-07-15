//go:build linux

package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBuildRsyncTreeManifestCanonicalizesEntriesAndCapturesInodeEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "beta"), []byte("same-content"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "beta"), filepath.Join(root, "alpha")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("alpha", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	limits := ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}
	manifest, err := buildRsyncTreeManifest(context.Background(), rootFD, limits)
	if err != nil {
		t.Fatal(err)
	}
	wantContent := sha256.Sum256([]byte("same-content"))
	if manifest.DigestAlgorithm != "sha256" || manifest.EntryCount != 3 || manifest.LogicalBytes != uint64(len("same-content")*2) || len(manifest.Encoded) == 0 {
		t.Fatalf("manifest summary=%+v", manifest)
	}
	if got := []string{manifest.Entries[0].RelativePath, manifest.Entries[1].RelativePath, manifest.Entries[2].RelativePath}; !reflect.DeepEqual(got, []string{"alpha", "beta", "link"}) {
		t.Fatalf("manifest ordering=%q", got)
	}
	alpha, beta := manifest.Entries[0], manifest.Entries[1]
	if alpha.ContentDigest != hex.EncodeToString(wantContent[:]) || beta.ContentDigest != alpha.ContentDigest || alpha.Device != beta.Device || alpha.Inode != beta.Inode || alpha.Nlink != 2 || beta.Nlink != 2 {
		t.Fatalf("hardlink manifest evidence alpha=%+v beta=%+v", alpha, beta)
	}
	if link := manifest.Entries[2]; link.Kind != rsyncTreeManifestSymlink || link.LinkTarget != "alpha" || link.ContentDigest != "" {
		t.Fatalf("symlink manifest evidence=%+v", link)
	}
	second, err := buildRsyncTreeManifest(context.Background(), rootFD, limits)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Digest != second.Digest || string(manifest.Encoded) != string(second.Encoded) {
		t.Fatalf("manifest bytes are not canonical first=%s second=%s", manifest.Encoded, second.Encoded)
	}
}

func TestRsyncTreeFidelityRejectsExternalFullCopyLinksAndSilentHardlinkFallback(t *testing.T) {
	limits := ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}
	t.Run("full copy external inode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "file"), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.Link(filepath.Join(root, "file"), outside); err != nil {
			t.Fatal(err)
		}
		manifest := rsyncTreeManifestForTest(t, root, limits)
		if err := validateRsyncTreeFullCopyFidelity(manifest); err == nil {
			t.Fatal("full-copy manifest accepted an externally shared inode")
		}
	})

	t.Run("hardlink fallback", func(t *testing.T) {
		parent := t.TempDir()
		candidate := t.TempDir()
		if err := os.WriteFile(filepath.Join(parent, "same"), []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, "changed"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := rsyncTreeManifestForTest(t, parent, limits)
		if err := os.WriteFile(filepath.Join(candidate, "same"), []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		parentInfo, err := os.Stat(filepath.Join(parent, "same"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(candidate, "same"), parentInfo.ModTime(), parentInfo.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(candidate, "changed"), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		after := rsyncTreeManifestForTest(t, parent, limits)
		candidateManifest := rsyncTreeManifestForTest(t, candidate, limits)
		if err := validateRsyncTreeHardlinkFidelity(before, after, candidateManifest); err == nil {
			t.Fatal("hardlink fidelity accepted a silent copy fallback")
		}
	})
}

func TestRsyncTreeHardlinkFidelityAcceptsSharedUnchangedAndIndependentChangedFiles(t *testing.T) {
	limits := ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}
	parent := t.TempDir()
	candidate := t.TempDir()
	for name, content := range map[string]string{"same": "same", "changed": "old", "deleted": "old-only"} {
		if err := os.WriteFile(filepath.Join(parent, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := rsyncTreeManifestForTest(t, parent, limits)
	if err := os.Link(filepath.Join(parent, "same"), filepath.Join(candidate, "same")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "changed"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := rsyncTreeManifestForTest(t, parent, limits)
	candidateManifest := rsyncTreeManifestForTest(t, candidate, limits)
	if err := validateRsyncTreeHardlinkFidelity(before, after, candidateManifest); err != nil {
		t.Fatalf("hardlink fidelity rejected valid candidate: %v", err)
	}
	entries := make(map[string]rsyncTreeManifestEntry, len(candidateManifest.Entries))
	for _, entry := range candidateManifest.Entries {
		entries[entry.RelativePath] = entry
	}
	parentEntries := make(map[string]rsyncTreeManifestEntry, len(before.Entries))
	for _, entry := range before.Entries {
		parentEntries[entry.RelativePath] = entry
	}
	if entries["same"].Inode != parentEntries["same"].Inode || entries["changed"].Inode == parentEntries["changed"].Inode {
		t.Fatalf("unexpected inode relationships parent=%+v candidate=%+v", parentEntries, entries)
	}
	if _, exists := entries["deleted"]; exists {
		t.Fatal("parent-only deleted entry leaked into candidate")
	}
	if content, err := os.ReadFile(filepath.Join(parent, "changed")); err != nil || string(content) != "old" {
		t.Fatalf("parent changed content=%q err=%v", content, err)
	}
}

func TestBuildRsyncTreeManifestRejectsLimitsAndSpecialNodes(t *testing.T) {
	t.Run("limits", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "one"), []byte("one"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "two"), []byte("two"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(fd) }()
		limits := ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 1, MaxRecordBytes: 4096, MaxDepth: 10}
		if _, err := buildRsyncTreeManifest(context.Background(), fd, limits); !errors.Is(err, errRsyncManagedTreeUnsafe) {
			t.Fatalf("entry-limited manifest error=%v, want unsafe", err)
		}
	})

	t.Run("special node", func(t *testing.T) {
		root := t.TempDir()
		if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(fd) }()
		limits := ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}
		if _, err := buildRsyncTreeManifest(context.Background(), fd, limits); !errors.Is(err, errRsyncManagedTreeUnsafe) {
			t.Fatalf("special-node manifest error=%v, want unsafe", err)
		}
	})
}

func rsyncTreeManifestForTest(t *testing.T, root string, limits ManifestLimits) rsyncTreeManifest {
	t.Helper()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	manifest, err := buildRsyncTreeManifest(context.Background(), fd, limits)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}
