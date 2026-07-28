package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"

	"golang.org/x/sys/unix"
)

func TestRsyncTreePreflightBuildsBoundEvidenceFromTrustedRoot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, markerDigest := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
	request.LocalSourceRoot = t.TempDir()

	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return now }, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := preflighter.Preflight(context.Background(), tree, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := backupasset.ValidateOpaqueID(evidence.ID); err != nil {
		t.Fatalf("preflight ID=%q: %v", evidence.ID, err)
	}
	if evidence.Mode != backupasset.PublicationVersionedFullCopy || evidence.TaskID != 7 || evidence.ExpectedTaskRevision != 3 || !evidence.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("preflight binding drifted: %+v", evidence)
	}
	if evidence.RepositoryMarkerDigest != markerDigest || len(evidence.ManagedRootIdentityDigest) != 64 || len(evidence.Digest) != 64 {
		t.Fatalf("preflight identity evidence is incomplete: %+v", evidence)
	}
	if !evidence.RenameNoReplaceVerified || !evidence.DirectoryFsyncVerified || evidence.FreeBytes == 0 || evidence.FreeInodes == 0 {
		t.Fatalf("preflight capability evidence is incomplete: %+v", evidence)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{tree.RootPath(), markerDigest, evidence.ManagedRootIdentityDigest, evidence.SourceIdentityDigest, "free_bytes", "free_inodes"} {
		if bytes.Contains(encoded, []byte(unsafe)) {
			t.Fatalf("preflight JSON leaked internal filesystem evidence %q: %s", unsafe, encoded)
		}
	}
}

func TestBootstrapRsyncManagedRootCreatesAndRevalidatesOwnedLayout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed Rsync tree bootstrap is Linux-only")
	}
	root := filepath.Join(t.TempDir(), "legacy.xirang-rsync-v1")
	request := RsyncManagedRootBootstrapRequest{
		ManagedRoot: root, RepositoryID: strings.Repeat("a", 32), MarkerKey: bytes.Repeat([]byte{0x42}, 32),
		CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}
	first, err := BootstrapRsyncManagedRoot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !validRsyncTreeDigest(first.RepositoryMarkerDigest) || !validRsyncTreeDigest(first.ManagedRootIdentityDigest) {
		t.Fatalf("bootstrap evidence=%+v", first)
	}
	for _, component := range []string{"repository.json", "staging", "points"} {
		if _, err := os.Lstat(filepath.Join(root, component)); err != nil {
			t.Fatalf("bootstrap did not create %s: %v", component, err)
		}
	}

	second, err := BootstrapRsyncManagedRoot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.RepositoryMarkerDigest != first.RepositoryMarkerDigest || second.ManagedRootIdentityDigest != first.ManagedRootIdentityDigest {
		t.Fatalf("bootstrap revalidation drifted: first=%+v second=%+v", first, second)
	}

	request.RepositoryID = strings.Repeat("b", 32)
	if _, err := BootstrapRsyncManagedRoot(context.Background(), request); err == nil {
		t.Fatal("bootstrap accepted an existing root for a different repository")
	}
}

func TestValidateRsyncManagedRootSeparationRejectsLegacyOverlap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed Rsync tree validation is Linux-only")
	}
	parent := t.TempDir()
	managedRoot := filepath.Join(parent, "legacy.xirang-rsync-v1")
	if _, err := BootstrapRsyncManagedRoot(context.Background(), RsyncManagedRootBootstrapRequest{
		ManagedRoot: managedRoot, RepositoryID: strings.Repeat("a", 32), MarkerKey: bytes.Repeat([]byte{0x42}, 32), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, legacyTarget := range []string{managedRoot, parent} {
		if err := ValidateRsyncManagedRootSeparation(context.Background(), managedRoot, legacyTarget); err == nil {
			t.Fatalf("legacy target %q overlapped managed root", legacyTarget)
		}
	}
	legacyTarget := filepath.Join(parent, "legacy")
	if err := os.Mkdir(legacyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRsyncManagedRootSeparation(context.Background(), managedRoot, legacyTarget); err != nil {
		t.Fatalf("separate legacy target rejected: %v", err)
	}
}

func TestRsyncTreePreflighterPreflightManagedRootUsesOnlyTrustedRootPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed Rsync tree preflight is Linux-only")
	}
	root := filepath.Join(t.TempDir(), "legacy.xirang-rsync-v1")
	bootstrap, err := BootstrapRsyncManagedRoot(context.Background(), RsyncManagedRootBootstrapRequest{
		ManagedRoot: root, RepositoryID: strings.Repeat("a", 32), MarkerKey: bytes.Repeat([]byte{0x42}, 32), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := preflighter.PreflightManagedRoot(context.Background(), root, RsyncTreePreflightRequest{
		TaskID: 7, ExpectedTaskRevision: 3, Mode: backupasset.PublicationVersionedFullCopy,
		LocalSourceRoot: source, RepositoryMarkerDigest: bootstrap.RepositoryMarkerDigest, CapabilityRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ManagedRootIdentityDigest != bootstrap.ManagedRootIdentityDigest {
		t.Fatalf("managed-root preflight identity=%q, want %q", evidence.ManagedRootIdentityDigest, bootstrap.ManagedRootIdentityDigest)
	}
}

func TestRsyncTreePreflightRejectsHardlinkEPERM(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedHardlink)
	request.LocalSourceRoot = t.TempDir()
	tree.linkat = func(int, string, int, string, int) error { return unix.EPERM }
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preflighter.Preflight(context.Background(), tree, request)
	if !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("hardlink EPERM error=%v, want managed-tree safety error", err)
	}
}

func TestRsyncTreePreflightRejectsRenameEXDEV(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
	request.LocalSourceRoot = t.TempDir()
	tree.renameat2 = func(int, string, int, string, uint) error { return unix.EXDEV }
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preflighter.Preflight(context.Background(), tree, request)
	if !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("rename EXDEV error=%v, want managed-tree safety error", err)
	}
}

func TestRsyncTreePreflightRejectsInsufficientCapacity(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
	request.LocalSourceRoot = t.TempDir()
	tree.statfs = func(_ int, stat *rsyncTreeFilesystemStats) error {
		stat.BlockSize = 1024
		stat.AvailableBlocks = 1
		stat.FreeInodes = 1
		return nil
	}
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Capacity = RsyncTreeCapacityRequirement{RequiredFreeBytes: 2 * 1024, RequiredFreeInodes: 2}
	_, err = preflighter.Preflight(context.Background(), tree, request)
	if !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("insufficient capacity error=%v, want managed-tree safety error", err)
	}
}

func TestRsyncTreePreflightRejectsLocalSourceOverlap(t *testing.T) {
	for _, sourceKind := range []string{"same", "ancestor", "descendant"} {
		t.Run(sourceKind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "managed")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			tree := newRsyncManagedTreeForTest(t, root)
			request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
			switch sourceKind {
			case "same":
				request.LocalSourceRoot = root
			case "ancestor":
				request.LocalSourceRoot = parent
			case "descendant":
				request.LocalSourceRoot = filepath.Join(root, "staging")
			}
			preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			_, err = preflighter.Preflight(context.Background(), tree, request)
			if !errors.Is(err, errRsyncManagedTreeUnsafe) {
				t.Fatalf("source overlap error=%v, want managed-tree safety error", err)
			}
		})
	}
}

func TestRsyncTreePreflightRejectsMarkerOrSourceSymlinkDrift(t *testing.T) {
	t.Run("marker digest", func(t *testing.T) {
		tree := newRsyncManagedTreeForTest(t, t.TempDir())
		request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
		request.LocalSourceRoot = t.TempDir()
		request.RepositoryMarkerDigest = strings.Repeat("0", 64)
		preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := preflighter.Preflight(context.Background(), tree, request); !errors.Is(err, errRsyncManagedTreeUnsafe) {
			t.Fatalf("marker drift error=%v, want managed-tree safety error", err)
		}
	})

	t.Run("source symlink", func(t *testing.T) {
		tree := newRsyncManagedTreeForTest(t, t.TempDir())
		request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "source")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		request.LocalSourceRoot = link
		preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := preflighter.Preflight(context.Background(), tree, request); !errors.Is(err, errRsyncManagedTreeUnsafe) {
			t.Fatalf("source symlink error=%v, want managed-tree safety error", err)
		}
	})
}

func TestRsyncTreePreflightRejectsFsyncFailure(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
	request.LocalSourceRoot = t.TempDir()
	tree.fsync = func(int) error { return unix.EIO }
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preflighter.Preflight(context.Background(), tree, request); !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("fsync failure error=%v, want managed-tree safety error", err)
	}
}

func TestRsyncTreePreflightRejectsParentLinkSafetyCeiling(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedHardlink)
	request.LocalSourceRoot = t.TempDir()
	request.Capacity = RsyncTreeCapacityRequirement{ParentLinkCount: 100, LinkSafetyCeiling: 100}
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preflighter.Preflight(context.Background(), tree, request); !errors.Is(err, errRsyncManagedTreeUnsafe) {
		t.Fatalf("link safety ceiling error=%v, want managed-tree safety error", err)
	}
}

func TestRsyncTreePreflightKeepsExplicitFullCopyModeWhenHardlinksUnavailable(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	request, _ := rsyncTreePreflightRequestForTest(t, tree, backupasset.PublicationVersionedFullCopy)
	request.LocalSourceRoot = t.TempDir()
	tree.linkat = func(int, string, int, string, int) error { return unix.EPERM }
	preflighter, err := NewRsyncTreePreflighter(func() time.Time { return time.Now().UTC() }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := preflighter.Preflight(context.Background(), tree, request)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Mode != backupasset.PublicationVersionedFullCopy || evidence.HardlinkVerified {
		t.Fatalf("full-copy preflight silently changed mode or capability: %+v", evidence)
	}
}

func rsyncTreePreflightRequestForTest(t *testing.T, tree *rsyncManagedTree, mode backupasset.TaskPublicationMode) (RsyncTreePreflightRequest, string) {
	t.Helper()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(tree.RootPath(), "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	return RsyncTreePreflightRequest{
		TaskID:                 7,
		ExpectedTaskRevision:   3,
		Mode:                   mode,
		LocalSourceRoot:        t.TempDir(),
		RepositoryMarkerDigest: markerDigest,
		CapabilityRevision:     1,
	}, markerDigest
}
