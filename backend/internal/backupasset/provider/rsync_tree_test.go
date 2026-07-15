package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRsyncManagedTreeRequiresFreshEmptyStaging(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	const attempt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := tree.CreateFreshStaging(attempt); err != nil {
		t.Fatal(err)
	}
	if err := tree.VerifyFreshStaging(attempt); err != nil {
		t.Fatalf("fresh staging verification: %v", err)
	}
	if err := tree.CreateFreshStaging(attempt); err == nil {
		t.Fatal("reused staging was accepted")
	}
	if err := os.WriteFile(filepath.Join(tree.RootPath(), "staging", attempt, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tree.VerifyFreshStaging(attempt); err == nil {
		t.Fatal("nonempty staging was accepted")
	}
	for _, unsafe := range []string{"../escape", "nested/attempt", ".", ""} {
		if err := tree.CreateFreshStaging(unsafe); err == nil {
			t.Fatalf("unsafe staging component %q was accepted", unsafe)
		}
	}
}

func TestRsyncManagedTreeProbesHardlinksAndCommitsNoReplace(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	probe, err := tree.ProbeCommitPrimitives()
	if err != nil {
		t.Fatal(err)
	}
	if !probe.HardlinkVerified || !probe.RenameNoReplaceVerified || !probe.DirectoryFsyncVerified {
		t.Fatalf("incomplete managed tree probe: %+v", probe)
	}

	const firstAttempt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const firstPoint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := tree.CreateFreshStaging(firstAttempt); err != nil {
		t.Fatal(err)
	}
	if err := tree.CommitStaging(firstAttempt, firstPoint); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree.RootPath(), "points", firstPoint)); err != nil {
		t.Fatalf("committed point missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree.RootPath(), "staging", firstAttempt)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains after commit: %v", err)
	}

	const secondAttempt = "cccccccccccccccccccccccccccccccc.dddddddddddddddddddddddddddddddd"
	if err := tree.CreateFreshStaging(secondAttempt); err != nil {
		t.Fatal(err)
	}
	if err := tree.CommitStaging(secondAttempt, firstPoint); err == nil {
		t.Fatal("commit overwrote existing final point")
	}
	if _, err := os.Stat(filepath.Join(tree.RootPath(), "points", firstPoint)); err != nil {
		t.Fatalf("existing final point changed after collision: %v", err)
	}
}

func TestRsyncManagedTreeCommitsPopulatedStaging(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	const attempt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const point = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := tree.CreateFreshStaging(attempt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree.RootPath(), "staging", attempt, "commit.json"), []byte("owned marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tree.CommitStaging(attempt, point); err != nil {
		t.Fatalf("commit populated staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree.RootPath(), "points", point, "commit.json")); err != nil {
		t.Fatalf("committed marker missing: %v", err)
	}
}

func TestRsyncManagedTreeCreatesDedicatedStagingTree(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	const attempt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := tree.CreateFreshStagingTree(attempt); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(tree.RootPath(), "staging", attempt, "tree")); err != nil || !info.IsDir() {
		t.Fatalf("staging tree info=%+v err=%v", info, err)
	}
	if err := tree.VerifyFreshStaging(attempt); err == nil {
		t.Fatal("staging with dedicated tree remained incorrectly fresh")
	}
}

func TestRsyncManagedTreeWritesStagingMetadataWithoutOverwrite(t *testing.T) {
	tree := newRsyncManagedTreeForTest(t, t.TempDir())
	const attempt = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := tree.CreateFreshStagingTree(attempt); err != nil {
		t.Fatal(err)
	}
	if err := tree.WriteStagingMetadata(attempt, "attempt.json", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := tree.WriteStagingMetadata(attempt, "attempt.json", []byte("second")); err == nil {
		t.Fatal("staging metadata overwrite was accepted")
	}
	content, err := os.ReadFile(filepath.Join(tree.RootPath(), "staging", attempt, "attempt.json"))
	if err != nil || string(content) != "first" {
		t.Fatalf("staging metadata content=%q err=%v", content, err)
	}
}

func TestRsyncManagedTreeRejectsSymlinkEscapeAndRootReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "managed")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := newRsyncManagedTreeForTest(t, root)
	if err := os.Remove(filepath.Join(root, "staging")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "staging")); err != nil {
		t.Fatal(err)
	}
	if err := tree.CreateFreshStaging("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err == nil {
		t.Fatal("symlinked staging directory was accepted")
	}

	oldRoot := filepath.Join(parent, "managed-old")
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := tree.VerifyRootIdentity(); err == nil {
		t.Fatal("root replacement was accepted")
	}
}

func newRsyncManagedTreeForTest(t *testing.T, root string) *rsyncManagedTree {
	t.Helper()
	tree, err := openRsyncManagedTree(root)
	if errors.Is(err, errRsyncManagedTreeUnsupported) {
		t.Skipf("managed-tree primitive unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	return tree
}
