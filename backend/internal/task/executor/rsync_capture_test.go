package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
)

func TestRsyncCaptureAndSelectionUseConfiguredBinary(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	wrapper := filepath.Join(t.TempDir(), "configured-rsync")
	script := fmt.Sprintf("#!/bin/sh\nexec %q \"$@\"\n", rsyncBinary)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write configured rsync wrapper: %v", err)
	}
	t.Setenv("PATH", filepath.Join(t.TempDir(), "no-rsync"))
	source := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), []byte("configured binary payload"), 0o600); err != nil {
		t.Fatalf("write source payload: %v", err)
	}
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  source + string(os.PathSeparator),
		RsyncTarget:  target,
		RsyncBinary:  wrapper,
	}
	raw, err := CaptureRsyncManifest(context.Background(), task, RsyncCaptureSourceRole)
	if err != nil {
		t.Fatalf("configured binary capture failed: %v", err)
	}
	if exitCode, runErr := (&RsyncExecutor{binary: wrapper}).Run(context.Background(), task, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("configured binary transfer failed: exit=%d err=%v", exitCode, runErr)
	}
	differences, err := RsyncSelectionDifferences(context.Background(), task, false)
	if err != nil || differences != 0 {
		t.Fatalf("configured binary selection comparison differences=%d err=%v", differences, err)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err != nil {
		t.Fatalf("configured binary target evidence failed: %v", err)
	}
}

func TestRsyncCaptureManifestMatchesTransferAndDetectsMutation(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	source := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "included.txt"), []byte("captured bytes"), 0o600); err != nil {
		t.Fatalf("write included file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "excluded.txt"), []byte("must not transfer"), 0o600); err != nil {
		t.Fatalf("write excluded file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatalf("create empty directory: %v", err)
	}
	if err := os.Symlink("included.txt", filepath.Join(source, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  source + string(os.PathSeparator),
		RsyncTarget:  target,
		Policy:       &model.Policy{ExcludeRules: "excluded.txt"},
	}
	raw, err := CaptureRsyncManifest(context.Background(), task, RsyncCaptureSourceRole)
	if err != nil {
		t.Fatalf("capture manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Layout != model.TaskRunCaptureLayoutDirectoryContents {
		t.Fatalf("layout = %q, want directory contents", manifest.Layout)
	}
	if len(manifest.Entries) != 4 {
		t.Fatalf("entry count = %d, want root, file, empty directory, symlink", len(manifest.Entries))
	}

	if exitCode, runErr := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), task, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("transfer failed: exit=%d err=%v", exitCode, runErr)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err != nil {
		t.Fatalf("matching target rejected: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := VerifyRsyncCaptureManifestTarget(canceledCtx, task, raw); err == nil {
		t.Fatal("canceled capture verification unexpectedly passed")
	}
	if err := os.WriteFile(filepath.Join(target, "included.txt"), []byte("mutated"), 0o600); err != nil {
		t.Fatalf("mutate target: %v", err)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, raw); err == nil {
		t.Fatal("mutated target unexpectedly passed capture verification")
	}
}

func TestRsyncCaptureVerificationRootStaysPinnedAfterSourceRename(t *testing.T) {
	allowed := t.TempDir()
	source := filepath.Join(allowed, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("original pinned source")
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	manifest := model.RsyncCaptureManifest{
		Layout: model.TaskRunCaptureLayoutDirectoryContents,
		Entries: []model.RsyncCaptureManifestEntry{
			{Path: "", Kind: "directory"},
			{Path: "payload.txt", Kind: "file", Size: int64(len(original)), SHA256: hex.EncodeToString(digest[:])},
		},
	}
	raw, err := model.EncodeRsyncCaptureManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatal(err)
	}

	root, base, err := openRsyncCaptureVerificationRoot(source, []string{allowed}, true)
	if err != nil {
		t.Fatalf("open pinned source root: %v", err)
	}
	defer func() { _ = root.Close() }()
	if base != "." {
		t.Fatalf("pinned directory base=%q, want .", base)
	}
	if err := os.Rename(source, filepath.Join(allowed, "renamed-source")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), []byte("replacement source"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range decoded.Entries {
		if err := verifyRsyncRootEntry(context.Background(), root, rsyncCaptureManifestSourcePath(base, entry.Path), entry, "source"); err != nil {
			t.Fatalf("verify renamed source entry %q through pinned root: %v", entry.Path, err)
		}
	}
}

func TestRsyncCaptureSourceKindRejectsEscapingIntermediateSymlink(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	outsideSource := filepath.Join(outside, "source")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSource, filepath.Join(allowed, "source")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, allowed)
	task := model.Task{RsyncSource: filepath.Join(allowed, "source", "payload")}
	if _, err := rsyncCaptureSourceKind(context.Background(), task, task.RsyncSource, RsyncCaptureSourceRole); err == nil {
		t.Fatal("source kind inspection followed intermediate symlink outside configured root")
	}
}

func TestRsyncCaptureSourceKindUsesUsableOverlappingRoot(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	outsideSource := filepath.Join(outside, "source")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicitRoot := filepath.Join(allowed, "source")
	if err := os.Symlink(outsideSource, explicitRoot); err != nil {
		t.Fatal(err)
	}
	task := model.Task{RsyncSource: filepath.Join(explicitRoot, "payload")}
	for _, test := range []struct {
		name  string
		roots string
	}{
		{name: "broad-root-first", roots: fmt.Sprintf("%s,%s", allowed, explicitRoot)},
		{name: "symlink-root-first", roots: fmt.Sprintf("%s,%s", explicitRoot, allowed)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, test.roots)
			kind, err := rsyncCaptureSourceKind(context.Background(), task, task.RsyncSource, RsyncCaptureSourceRole)
			if err != nil {
				t.Fatalf("source kind through explicitly allowed symlink root: %v", err)
			}
			if kind != "file" {
				t.Fatalf("source kind=%q, want file", kind)
			}
		})
	}
}

func TestCaptureRsyncManifestExactSymlinkRootLayouts(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for exact symlink-root capture", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("exact symlink-root helper unavailable: %v", statErr)
	}
	allowed := t.TempDir()
	outside := t.TempDir()
	explicitRoot := filepath.Join(allowed, "source")
	outsideSource := filepath.Join(outside, "source")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("exact symlink-root payload")
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSource, explicitRoot); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.HelperPathEnv, helper)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, t.TempDir())
	for _, roots := range []struct {
		name string
		raw  string
	}{
		{name: "broad-root-first", raw: fmt.Sprintf("%s,%s", allowed, explicitRoot)},
		{name: "symlink-root-first", raw: fmt.Sprintf("%s,%s", explicitRoot, allowed)},
	} {
		t.Run(roots.name, func(t *testing.T) {
			t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, roots.raw)
			for _, layout := range []struct {
				name     string
				source   string
				expected string
				root     string
			}{
				{name: "directory-root", source: explicitRoot, expected: model.TaskRunCaptureLayoutDirectoryRoot, root: filepath.Base(explicitRoot)},
				{name: "directory-contents", source: explicitRoot + string(filepath.Separator), expected: model.TaskRunCaptureLayoutDirectoryContents},
			} {
				t.Run(layout.name, func(t *testing.T) {
					raw, captureErr := CaptureRsyncManifest(context.Background(), model.Task{
						ExecutorType: "rsync",
						RsyncSource:  layout.source,
						RsyncTarget:  filepath.Join(t.TempDir(), "capture"),
						RsyncBinary:  rsyncBinary,
					}, RsyncCaptureSourceRole)
					if captureErr != nil {
						t.Fatalf("exact symlink-root capture failed: %v", captureErr)
					}
					manifest, decodeErr := model.DecodeRsyncCaptureManifest(raw)
					if decodeErr != nil {
						t.Fatalf("decode exact symlink-root manifest: %v", decodeErr)
					}
					if manifest.Layout != layout.expected || manifest.Root != layout.root {
						t.Fatalf("layout=%q root=%q, want layout=%q root=%q", manifest.Layout, manifest.Root, layout.expected, layout.root)
					}
					for _, entry := range manifest.Entries {
						if entry.Path == "payload" && entry.Kind == "file" && entry.Size == int64(len(payload)) {
							return
						}
					}
					t.Fatalf("exact symlink-root manifest omitted payload: %+v", manifest.Entries)
				})
			}
		})
	}
}

func TestRsyncCaptureSourceKindRejectsOutsideAllRoots(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	outsideSource := filepath.Join(outside, "source")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, allowed)
	task := model.Task{RsyncSource: filepath.Join(outsideSource, "payload")}
	if _, err := rsyncCaptureSourceKind(context.Background(), task, task.RsyncSource, RsyncCaptureSourceRole); err == nil {
		t.Fatal("source kind inspection accepted a path outside every configured root")
	}
}

func TestVerifyRsyncCaptureManifestTargetRejectsEscapingIntermediateSymlink(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "target")
	if err := os.MkdirAll(outsideTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("outside target")
	if err := os.WriteFile(filepath.Join(outsideTarget, "payload.txt"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(allowed, "pivot")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(allowed, "pivot", "target")
	digest := sha256.Sum256(payload)
	raw, err := model.EncodeRsyncCaptureManifest(model.RsyncCaptureManifest{
		Layout: model.TaskRunCaptureLayoutDirectoryContents,
		Entries: []model.RsyncCaptureManifestEntry{
			{Path: "", Kind: "directory"},
			{Path: "payload.txt", Kind: "file", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, allowed)
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), model.Task{RsyncTarget: target}, raw); err == nil {
		t.Fatal("target evidence followed intermediate symlink outside configured root")
	}
}
