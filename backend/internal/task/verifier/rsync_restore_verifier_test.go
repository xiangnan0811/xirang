package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"
)

// TestRsyncRestoreVerifierRealExecutorRoundTrip exercises the complete legacy
// Rsync restore path.  It deliberately uses an sshd subprocess rather than
// mocking either side of the transfer: the source is on the node, the backup
// target is Core-local, and the restore target is read back over SSH.
func TestRsyncRestoreVerifierRealExecutorRoundTrip(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)

	cases := []struct {
		name             string
		kind             string
		trailingSlash    bool
		coreTargetExists bool
		nodeTargetExists bool
	}{
		{
			name:             "directory-root-target-absent",
			kind:             "directory",
			coreTargetExists: false,
			nodeTargetExists: false,
		},
		{
			name:             "directory-root-target-existing",
			kind:             "directory",
			coreTargetExists: true,
			nodeTargetExists: true,
		},
		{
			name:             "directory-contents-target-absent",
			kind:             "directory",
			trailingSlash:    true,
			coreTargetExists: false,
			nodeTargetExists: false,
		},
		{
			name:             "directory-contents-target-existing",
			kind:             "directory",
			trailingSlash:    true,
			coreTargetExists: true,
			nodeTargetExists: true,
		},
		{
			name:             "single-file-core-target-absent",
			kind:             "single-file",
			coreTargetExists: false,
			nodeTargetExists: false,
		},
		{
			name:             "single-file-core-target-absent-node-target-existing",
			kind:             "single-file",
			coreTargetExists: false,
			nodeTargetExists: true,
		},
		{
			name:             "single-file-core-target-existing",
			kind:             "single-file",
			coreTargetExists: true,
			nodeTargetExists: true,
		},
		{
			name:             "empty-directory",
			kind:             "empty-directory",
			coreTargetExists: false,
			nodeTargetExists: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRsyncRestoreFixture(t, node, rsyncBinary, tc.name, tc.kind, tc.trailingSlash, nil, tc.coreTargetExists, tc.nodeTargetExists)
			assertRsyncFixtureManifest(t, fixture)
			assertRsyncCoreLayout(t, fixture)

			result := Verify(context.Background(), fixture.restoreTask, 0, setupTestDB(t), func(string, string) {}, true)
			if result.Status != "passed" {
				t.Fatalf("real Run/RunRestore restore verification status=%q message=%q", result.Status, result.Message)
			}
			assertRsyncRemoteLayout(t, fixture)
			if tc.kind == "empty-directory" {
				if err := os.RemoveAll(fixture.target); err != nil {
					t.Fatal(err)
				}
				assertRsyncVerifyWarning(t, fixture, "missing empty restore directory")
			}
		})
	}
}

// TestRsyncRestoreVerifierRejectsMissingTamperedAndDecoyEvidence proves that
// a warning is returned for every failed evidence path.  In particular, an
// unrelated node directory with the same basename is never accepted as the
// requested restore target.
func TestRsyncRestoreVerifierRejectsMissingTamperedAndDecoyEvidence(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	fixture := newRsyncRestoreFixture(t, node, rsyncBinary, "negative-directory", "directory", false, nil, false, false)

	assertRsyncVerifyPassed(t, fixture)

	coreKeepPath := rsyncRestoreCorePath(fixture, "keep.txt")
	if err := os.WriteFile(coreKeepPath, []byte("tampered Core"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "tampered Core source")
	if exitCode, runErr := fixture.runner.Run(context.Background(), fixture.backupTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("rebuild Core source after tamper exit=%d err=%v", exitCode, runErr)
	}
	if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("restore target after Core tamper exit=%d err=%v", exitCode, runErr)
	}
	if os.Geteuid() != 0 {
		coreRootPath := rsyncRestoreCorePath(fixture, "")
		if err := os.Chmod(coreRootPath, 0); err != nil {
			t.Fatal(err)
		}
		assertRsyncVerifyWarning(t, fixture, "unreadable Core source")
		if err := os.Chmod(coreRootPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	keepPath := rsyncRestoreTargetPath(fixture, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("tampered target"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "tampered target")

	if err := os.Remove(keepPath); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "missing target file")
	if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("restore target before missing-directory check exit=%d err=%v", exitCode, runErr)
	}
	if err := os.RemoveAll(fixture.target); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "missing target directory")

	if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("restore target before Core-source check exit=%d err=%v", exitCode, runErr)
	}
	if err := os.RemoveAll(fixture.coreBackup); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "missing Core source")

	// Rebuild through the real backup and restore executors.  Then place the
	// same content under a wrong nesting level and assert that the requested
	// target still fails instead of being found by basename.
	if exitCode, runErr := fixture.runner.Run(context.Background(), fixture.backupTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("rebuild Core backup exit=%d err=%v", exitCode, runErr)
	}
	if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("restore target before wrong-nesting check exit=%d err=%v", exitCode, runErr)
	}
	if err := os.RemoveAll(fixture.target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.target, filepath.Base(fixture.source)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyRsyncTree(t, filepath.Join(fixture.coreBackup, filepath.Base(fixture.source)), filepath.Join(fixture.target, filepath.Base(fixture.source))); err != nil {
		t.Fatal(err)
	}
	assertRsyncVerifyWarning(t, fixture, "wrong target nesting")

	if err := os.RemoveAll(fixture.target); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(filepath.Dir(fixture.target), "decoy", filepath.Base(fixture.target))
	if err := copyRsyncTree(t, filepath.Join(fixture.coreBackup, filepath.Base(fixture.source)), decoy); err != nil {
		t.Fatal(err)
	}
	if result := Verify(context.Background(), fixture.restoreTask, 0, setupTestDB(t), func(string, string) {}, true); result.Status == "passed" {
		t.Fatalf("same-name decoy unexpectedly passed: %+v", result)
	}
}

// TestRsyncRestoreVerifierAppliesExactExcludeSelectionToActualRestore checks
// file, directory, nested, anchored, wildcard, CRLF, and empty policies on
// the actual Rsync transfer and restore admission path.  A stale excluded
// Core entry is seeded before the restore so a rule that is not rebased from
// the directory-root layout becomes observable on the node.
func TestRsyncRestoreVerifierAppliesExactExcludeSelectionToActualRestore(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)

	cases := []struct {
		name          string
		rule          string
		excludedPaths []string
	}{
		{name: "file", rule: "excluded.env", excludedPaths: []string{"excluded.env"}},
		{name: "directory", rule: "cache", excludedPaths: []string{filepath.Join("cache", "ignored.txt")}},
		{name: "nested", rule: "logs/**", excludedPaths: []string{filepath.Join("nested", "logs", "ignored.log")}},
		{name: "anchored", rule: "/exclude-anchored-source/anchored.txt", excludedPaths: []string{"anchored.txt"}},
		{name: "wildcard", rule: "*.tmp", excludedPaths: []string{"scratch.tmp"}},
		{name: "crlf", rule: "excluded.env\r\nanchored.txt", excludedPaths: []string{"excluded.env", "anchored.txt"}},
		{name: "empty", rule: "", excludedPaths: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := &model.Policy{ExcludeRules: tc.rule}
			fixture := newRsyncRestoreFixture(t, node, rsyncBinary, "exclude-"+tc.name, "exclude-directory", false, policy, true, false)
			for _, excludedPath := range tc.excludedPaths {
				stale := rsyncRestoreCorePath(fixture, excludedPath)
				if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stale, []byte("stale excluded Core entry"), 0o644); err != nil {
					t.Fatal(err)
				}
				if manifestHasPath(fixture.manifest, excludedPath) {
					t.Fatalf("excluded source path %q was present in capture manifest: %+v", excludedPath, fixture.manifest)
				}
			}
			if len(tc.excludedPaths) > 0 {
				if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
					t.Fatalf("restore after stale Core entry exit=%d err=%v", exitCode, runErr)
				}
			} else if !manifestHasPath(fixture.manifest, "excluded.env") || !manifestHasPath(fixture.manifest, "anchored.txt") {
				t.Fatalf("empty policy unexpectedly excluded source entries: %+v", fixture.manifest)
			}

			assertRsyncVerifyPassed(t, fixture)
			for _, excludedPath := range tc.excludedPaths {
				if _, statErr := os.Lstat(rsyncRestoreTargetPath(fixture, excludedPath)); statErr == nil {
					t.Fatalf("excluded path %q reached restore target", excludedPath)
				}
			}
		})
	}
}

// TestRsyncRestoreVerifierFilteredEmptySelectionKeepsDirectoryIdentity covers
// a non-empty source whose exact Rsync selection is empty.  This is distinct
// from a physically empty source: filtering every child must still produce a
// real empty restore directory and a passing evidence check.
func TestRsyncRestoreVerifierFilteredEmptySelectionKeepsDirectoryIdentity(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	fixture := newRsyncRestoreFixture(t, node, rsyncBinary, "filtered-empty", "exclude-directory", false, &model.Policy{ExcludeRules: "/filtered-empty-source/*"}, false, false)
	stale := rsyncRestoreCorePath(fixture, "keep.txt")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale excluded Core sibling"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.target); err != nil {
		t.Fatal(err)
	}
	if exitCode, runErr := fixture.restorer.RunRestore(context.Background(), fixture.restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("restore filtered-empty target with stale Core sibling exit=%d err=%v", exitCode, runErr)
	}

	info, err := os.Stat(fixture.target)
	if err != nil {
		t.Fatalf("real restore target missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("filtered-empty restore target type=%v, want directory", info.Mode())
	}
	entries, err := os.ReadDir(fixture.target)
	if err != nil {
		t.Fatalf("enumerate filtered-empty restore target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("filtered-empty restore target is not empty: %v", entries)
	}
	assertRsyncRootMode(t, fixture.source, fixture.target)
	assertRsyncVerifyPassed(t, fixture)
}

func assertRsyncVerifyPassed(t *testing.T, fixture rsyncRestoreFixture) {
	t.Helper()
	result := Verify(context.Background(), fixture.restoreTask, 0, setupTestDB(t), func(string, string) {}, true)
	if result.Status != "passed" {
		t.Fatalf("restore verification status=%q message=%q", result.Status, result.Message)
	}
}

func assertRsyncVerifyWarning(t *testing.T, fixture rsyncRestoreFixture, reason string) {
	t.Helper()
	result := Verify(context.Background(), fixture.restoreTask, 0, setupTestDB(t), func(string, string) {}, true)
	if result.Status == "passed" {
		t.Fatalf("%s unexpectedly passed: %+v", reason, result)
	}
	if result.Status != "warning" {
		t.Fatalf("%s status=%q, want warning: %+v", reason, result.Status, result)
	}
}
func assertRsyncRootMode(t *testing.T, source, target string) {
	t.Helper()
	for label, path := range map[string]string{"source": source, "restored": target} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s root stat %q: %v", label, path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s root %q is not a directory", label, path)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s root %q mode=%#o, want %#o", label, path, got, 0o700)
		}
	}
}

type rsyncRestoreFixture struct {
	source      string
	coreBackup  string
	target      string
	backupTask  model.Task
	restoreTask model.Task
	manifest    model.RsyncCaptureManifest
	rawManifest string
	runner      executor.Executor
	restorer    executor.RestoreExecutor
	fileData    map[string]string
	entryKinds  map[string]string
}

func newRsyncRestoreFixture(t *testing.T, node model.Node, rsyncBinary, name, kind string, trailingSlash bool, policy *model.Policy, coreTargetExists, nodeTargetExists bool) rsyncRestoreFixture {
	t.Helper()
	nodeRoot := t.TempDir()
	coreRoot := t.TempDir()
	source := filepath.Join(nodeRoot, name+"-source")
	fileData, entryKinds := createRsyncFixtureTree(t, source, kind)
	if err := os.Chmod(source, 0o700); err != nil {
		t.Fatalf("set fixture source root mode: %v", err)
	}
	sourceOperand := source
	if trailingSlash {
		sourceOperand += string(filepath.Separator)
	}
	coreBackup := filepath.Join(coreRoot, name+"-backup")
	if coreTargetExists {
		if err := os.MkdirAll(coreBackup, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(nodeRoot, name+"-restore", "destination")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if nodeTargetExists {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	backupTask := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  sourceOperand,
		RsyncTarget:  coreBackup,
		Node:         node,
		Policy:       policy,
	}
	ctx := context.Background()
	raw, err := executor.CaptureRsyncManifest(ctx, backupTask)
	if err != nil {
		t.Fatalf("capture source manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode source manifest: %v", err)
	}
	backupRunner := executor.NewFactory(rsyncBinary).Resolve("rsync")
	if exitCode, runErr := backupRunner.Run(ctx, backupTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("real Rsync backup exit=%d err=%v", exitCode, runErr)
	}
	if err := executor.VerifyRsyncCaptureManifestTarget(ctx, backupTask, raw); err != nil {
		var paths []string
		_ = filepath.WalkDir(coreBackup, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry != nil {
				paths = append(paths, path)
			}
			return nil
		})
		t.Fatalf("Core target did not match source manifest: %v; manifest=%+v target=%q paths=%v", err, manifest, coreBackup, paths)
	}
	restorer, ok := backupRunner.(executor.RestoreExecutor)
	if !ok {
		t.Fatal("rsync factory result does not implement RestoreExecutor")
	}
	restoreTask := backupTask
	restoreTask.RsyncSource = coreBackup
	restoreTask.RsyncTarget = target
	restoreTask.RsyncCaptureLayout = manifest.Layout
	restoreTask.RsyncCaptureRoot = manifest.Root
	restoreTask.RsyncCaptureManifest = raw
	if exitCode, runErr := restorer.RunRestore(ctx, restoreTask, func(string, string) {}, nil); runErr != nil || exitCode != 0 {
		t.Fatalf("real Rsync restore exit=%d err=%v", exitCode, runErr)
	}
	return rsyncRestoreFixture{
		source:      source,
		coreBackup:  coreBackup,
		target:      target,
		backupTask:  backupTask,
		restoreTask: restoreTask,
		manifest:    manifest,
		rawManifest: raw,
		runner:      backupRunner,
		restorer:    restorer,
		fileData:    fileData,
		entryKinds:  entryKinds,
	}
}

func createRsyncFixtureTree(t *testing.T, source, kind string) (map[string]string, map[string]string) {
	t.Helper()
	files := make(map[string]string)
	entries := make(map[string]string)
	switch kind {
	case "single-file":
		files[""] = "single-file payload"
		entries[""] = "file"
		if err := os.WriteFile(source, []byte(files[""]), 0o644); err != nil {
			t.Fatal(err)
		}
	case "empty-directory":
		entries[""] = "directory"
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatal(err)
		}
	case "directory", "exclude-directory":
		files["keep.txt"] = "included payload"
		entries[""] = "directory"
		entries["keep.txt"] = "file"
		entries["nested"] = "directory"
		entries["nested/value.txt"] = "file"
		entries["nested/logs"] = "directory"
		entries["nested/logs/ignored.log"] = "file"
		entries["empty"] = "directory"
		files["nested/value.txt"] = "nested payload"
		files["nested/logs/ignored.log"] = "nested log payload"
		if kind == "exclude-directory" {
			entries["cache"] = "directory"
			files["cache/ignored.txt"] = "cache payload"
			entries["cache/ignored.txt"] = "file"
			files["excluded.env"] = "excluded payload"
			entries["excluded.env"] = "file"
			files["scratch.tmp"] = "wildcard payload"
			entries["scratch.tmp"] = "file"
			files["anchored.txt"] = "anchored payload"
			entries["anchored.txt"] = "file"
		}
		if err := os.MkdirAll(filepath.Join(source, "nested", "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		for relative, content := range files {
			path := filepath.Join(source, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown fixture kind %q", kind)
	}
	return files, entries
}

func assertRsyncFixtureManifest(t *testing.T, fixture rsyncRestoreFixture) {
	t.Helper()
	if len(fixture.manifest.Entries) != len(fixture.entryKinds) {
		t.Fatalf("manifest entries=%d, want %d: %+v", len(fixture.manifest.Entries), len(fixture.entryKinds), fixture.manifest)
	}
	for _, entry := range fixture.manifest.Entries {
		wantKind, ok := fixture.entryKinds[entry.Path]
		if !ok || entry.Kind != wantKind {
			t.Fatalf("unexpected manifest entry=%+v, expected kinds=%v", entry, fixture.entryKinds)
		}
		if entry.Kind == "file" {
			want := fixture.fileData[entry.Path]
			sum := sha256.Sum256([]byte(want))
			if entry.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("manifest SHA for %q=%q, want %q", entry.Path, entry.SHA256, hex.EncodeToString(sum[:]))
			}
		}
	}
}

func assertRsyncCoreLayout(t *testing.T, fixture rsyncRestoreFixture) {
	t.Helper()
	for relative, kind := range fixture.entryKinds {
		path := filepath.Join(fixture.coreBackup, filepath.FromSlash(relative))
		if fixture.manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot {
			path = filepath.Join(fixture.coreBackup, fixture.manifest.Root, filepath.FromSlash(relative))
		}
		if fixture.manifest.Layout == model.TaskRunCaptureLayoutSingleFile {
			path = fixture.coreBackup
			if fixture.manifest.Root != "" {
				path = filepath.Join(path, fixture.manifest.Root)
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Core relative path %q missing at %q: %v", relative, path, err)
		}
		if kind == "directory" && !info.IsDir() || kind == "file" && !info.Mode().IsRegular() {
			t.Fatalf("Core relative path %q has wrong type at %q", relative, path)
		}
	}
	if fixture.manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot && fixture.manifest.Root != "" {
		nestedRoot := filepath.Join(fixture.coreBackup, fixture.manifest.Root, fixture.manifest.Root)
		if _, err := os.Lstat(nestedRoot); err == nil {
			t.Fatalf("Core transfer unexpectedly nested logical root at %q", nestedRoot)
		}
	}
}

func assertRsyncRemoteLayout(t *testing.T, fixture rsyncRestoreFixture) {
	t.Helper()
	for relative, kind := range fixture.entryKinds {
		path := rsyncRestoreTargetPath(fixture, relative)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("node relative path %q missing at %q: %v", relative, path, err)
		}
		if kind == "directory" && !info.IsDir() || kind == "file" && !info.Mode().IsRegular() {
			t.Fatalf("node relative path %q has wrong type at %q", relative, path)
		}
	}
	if fixture.manifest.Layout != model.TaskRunCaptureLayoutSingleFile {
		if _, err := os.Lstat(filepath.Join(fixture.target, filepath.Base(fixture.target))); err == nil {
			t.Fatalf("node restore unexpectedly nested target basename")
		}
	}
}

func rsyncRestoreCorePath(fixture rsyncRestoreFixture, relative string) string {
	path := fixture.coreBackup
	if fixture.manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot && fixture.manifest.Root != "" {
		path = filepath.Join(path, fixture.manifest.Root)
	}
	if fixture.manifest.Layout == model.TaskRunCaptureLayoutSingleFile && fixture.manifest.Root != "" {
		path = filepath.Join(path, fixture.manifest.Root)
	}
	if relative != "" {
		path = filepath.Join(path, filepath.FromSlash(relative))
	}
	return path
}

func rsyncRestoreTargetPath(fixture rsyncRestoreFixture, relative string) string {
	path := fixture.target
	if fixture.manifest.Layout == model.TaskRunCaptureLayoutSingleFile {
		if fixture.manifest.Root != "" {
			path = filepath.Join(path, fixture.manifest.Root)
		} else if info, err := os.Lstat(path); err == nil && info.IsDir() {
			// With a plain Core file restored into an existing directory, Rsync
			// names the child after the resolved Core source, not manifest.Root.
			path = filepath.Join(path, filepath.Base(filepath.Clean(fixture.restoreTask.RsyncSource)))
		}
	}
	if relative != "" {
		path = filepath.Join(path, filepath.FromSlash(relative))
	}
	return path
}

func manifestHasPath(manifest model.RsyncCaptureManifest, path string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func copyRsyncTree(t *testing.T, source, target string) error {
	t.Helper()
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o755)
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
}
