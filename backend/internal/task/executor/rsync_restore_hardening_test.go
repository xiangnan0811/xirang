package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/testutil"

	"golang.org/x/sys/unix"
)

type rsyncRestoreTestFixture struct {
	source      string
	backup      string
	target      string
	restore     model.Task
	manifest    model.RsyncCaptureManifest
	rawManifest string
	rsyncBinary string
}

func newRsyncRestoreTestFixture(t *testing.T, node model.Node, rsyncBinary, layout string) rsyncRestoreTestFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	target := filepath.Join(root, "restore")
	var sourceOperand string
	switch layout {
	case model.TaskRunCaptureLayoutDirectoryContents, model.TaskRunCaptureLayoutDirectoryRoot:
		if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "selected.txt"), []byte("captured source"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "nested", "child.txt"), []byte("nested source"), 0o640); err != nil {
			t.Fatal(err)
		}
		if layout == model.TaskRunCaptureLayoutDirectoryRoot {
			if err := os.MkdirAll(backup, 0o755); err != nil {
				t.Fatal(err)
			}
			sourceOperand = source
		} else {
			sourceOperand = source + string(filepath.Separator)
		}
	case model.TaskRunCaptureLayoutSingleFile:
		if err := os.WriteFile(source, []byte("captured source"), 0o640); err != nil {
			t.Fatal(err)
		}
		sourceOperand = source
	default:
		t.Fatalf("unsupported fixture layout %q", layout)
	}
	captureTask := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  sourceOperand,
		RsyncTarget:  backup,
		RsyncBinary:  rsyncBinary,
	}
	rawManifest, err := CaptureRsyncManifest(context.Background(), captureTask)
	if err != nil {
		t.Fatalf("capture fixture manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(rawManifest)
	if err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	if code, err := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), captureTask, func(string, string) {}, nil); err != nil || code != 0 {
		t.Fatalf("seed Core backup code=%d err=%v", code, err)
	}
	if layout == model.TaskRunCaptureLayoutDirectoryContents || layout == model.TaskRunCaptureLayoutDirectoryRoot {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore := captureTask
	restore.Node = node
	restore.RsyncSource = backup
	restore.RsyncTarget = target
	restore.RsyncCaptureLayout = manifest.Layout
	restore.RsyncCaptureRoot = manifest.Root
	restore.RsyncCaptureManifest = rawManifest
	return rsyncRestoreTestFixture{
		source:      source,
		backup:      backup,
		target:      target,
		restore:     restore,
		manifest:    manifest,
		rawManifest: rawManifest,
		rsyncBinary: rsyncBinary,
	}
}

func rsyncRestoreFixturePath(fixture rsyncRestoreTestFixture, relative string) string {
	base := filepath.Clean(fixture.backup)
	switch fixture.manifest.Layout {
	case model.TaskRunCaptureLayoutDirectoryRoot:
		if fixture.manifest.Root != "" {
			base = filepath.Join(base, fixture.manifest.Root)
		}
	case model.TaskRunCaptureLayoutSingleFile:
		if fixture.manifest.Root != "" {
			base = filepath.Join(base, fixture.manifest.Root)
		}
	}
	if relative != "" {
		base = filepath.Join(base, filepath.FromSlash(relative))
	}
	return base
}

func snapshotRsyncPath(t *testing.T, path string) (exists bool, mode os.FileMode, modTime time.Time, data []byte) {
	t.Helper()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, 0, time.Time{}, nil
	}
	if err != nil {
		t.Fatalf("snapshot %q: %v", path, err)
	}
	if info.Mode().IsRegular() {
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("snapshot file %q: %v", path, err)
		}
	}
	return true, info.Mode(), info.ModTime(), data
}

func assertRsyncPathSnapshot(t *testing.T, path string, exists bool, mode os.FileMode, modTime time.Time, data []byte) {
	t.Helper()
	gotExists, gotMode, gotModTime, gotData := snapshotRsyncPath(t, path)
	if gotExists != exists {
		t.Fatalf("path %q exists=%v, want %v", path, gotExists, exists)
	}
	if !exists {
		return
	}
	if gotMode != mode {
		t.Fatalf("path %q mode=%s, want %s", path, gotMode, mode)
	}
	if !gotModTime.Equal(modTime) {
		t.Fatalf("path %q mtime=%s, want %s", path, gotModTime, modTime)
	}
	if string(gotData) != string(data) {
		t.Fatalf("path %q data=%q, want %q", path, gotData, data)
	}
}

func TestRsyncRestoreRejectsChangedSourceBeforeRemoteWrite(t *testing.T) {
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
	for _, layout := range []string{
		model.TaskRunCaptureLayoutDirectoryContents,
		model.TaskRunCaptureLayoutDirectoryRoot,
		model.TaskRunCaptureLayoutSingleFile,
	} {
		t.Run(layout, func(t *testing.T) {
			fixture := newRsyncRestoreTestFixture(t, node, rsyncBinary, layout)
			selected := rsyncRestoreFixturePath(fixture, "selected.txt")
			if layout == model.TaskRunCaptureLayoutSingleFile {
				selected = rsyncRestoreFixturePath(fixture, "")
			}
			if err := os.WriteFile(selected, []byte("corrupt source"), 0o640); err != nil {
				t.Fatalf("tamper Core source: %v", err)
			}
			if layout == model.TaskRunCaptureLayoutSingleFile {
				if err := os.MkdirAll(filepath.Dir(fixture.target), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(fixture.target, "sentinel"), []byte("must remain"), 0o600); err != nil {
				t.Fatal(err)
			}
			if layout == model.TaskRunCaptureLayoutSingleFile {
				if err := os.WriteFile(fixture.target, []byte("original target"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			targetSnapshotExists, targetSnapshotMode, targetSnapshotTime, targetSnapshotData := snapshotRsyncPath(t, fixture.target)
			if _, restoreErr := (&RsyncExecutor{binary: rsyncBinary}).RunRestore(context.Background(), fixture.restore, func(string, string) {}, nil); restoreErr == nil {
				t.Fatal("changed source unexpectedly restored")
			}
			assertRsyncPathSnapshot(t, fixture.target, targetSnapshotExists, targetSnapshotMode, targetSnapshotTime, targetSnapshotData)
			if err := os.RemoveAll(fixture.target); err != nil {
				t.Fatal(err)
			}
			if _, restoreErr := (&RsyncExecutor{binary: rsyncBinary}).RunRestore(context.Background(), fixture.restore, func(string, string) {}, nil); restoreErr == nil {
				t.Fatal("changed source unexpectedly restored into absent target")
			}
			assertRsyncPathSnapshot(t, fixture.target, false, 0, time.Time{}, nil)

			if layout == model.TaskRunCaptureLayoutSingleFile {
				if err := os.WriteFile(fixture.target, []byte("missing source target"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(fixture.target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.target, "sentinel"), []byte("missing source target"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			missingSourceSnapshotExists, missingSourceSnapshotMode, missingSourceSnapshotTime, missingSourceSnapshotData := snapshotRsyncPath(t, fixture.target)
			if err := os.RemoveAll(fixture.backup); err != nil {
				t.Fatal(err)
			}
			if _, restoreErr := (&RsyncExecutor{binary: rsyncBinary}).RunRestore(context.Background(), fixture.restore, func(string, string) {}, nil); restoreErr == nil {
				t.Fatal("missing source unexpectedly restored")
			}
			assertRsyncPathSnapshot(t, fixture.target, missingSourceSnapshotExists, missingSourceSnapshotMode, missingSourceSnapshotTime, missingSourceSnapshotData)
		})
	}
}

func TestRsyncRestoreChecksumHealsSameSizeSameTimeTarget(t *testing.T) {
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
	for _, layout := range []string{model.TaskRunCaptureLayoutDirectoryContents, model.TaskRunCaptureLayoutSingleFile} {
		t.Run(layout, func(t *testing.T) {
			fixture := newRsyncRestoreTestFixture(t, node, rsyncBinary, layout)
			source := rsyncRestoreFixturePath(fixture, "selected.txt")
			if layout == model.TaskRunCaptureLayoutSingleFile {
				source = rsyncRestoreFixturePath(fixture, "")
			}
			sourceInfo, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			if layout == model.TaskRunCaptureLayoutSingleFile {
				if err := os.WriteFile(fixture.target, []byte("wrong content!!"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(fixture.target, "selected.txt"), []byte("wrong content!!"), 0o600); err != nil {
				t.Fatal(err)
			}
			wrongTarget := fixture.target
			if layout == model.TaskRunCaptureLayoutDirectoryContents {
				wrongTarget = filepath.Join(fixture.target, "selected.txt")
			}
			if err := os.Chtimes(wrongTarget, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
			if code, err := (&RsyncExecutor{binary: rsyncBinary}).RunRestore(context.Background(), fixture.restore, func(string, string) {}, nil); err != nil || code != 0 {
				t.Fatalf("checksum restore code=%d err=%v", code, err)
			}
			got, err := os.ReadFile(wrongTarget)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "captured source" {
				t.Fatalf("restored content=%q, want %q", got, "captured source")
			}
		})
	}
}

func TestRsyncRestoreUsesVerifiedStagingAcrossSourceMutation(t *testing.T) {
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
	fixture := newRsyncRestoreTestFixture(t, node, rsyncBinary, model.TaskRunCaptureLayoutDirectoryContents)
	wrapper := filepath.Join(t.TempDir(), "mutating-rsync.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'mutated source' > %q\nexec %q \"$@\"\n", fixture.backup+string(filepath.Separator)+"selected.txt", rsyncBinary)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.restore.RsyncBinary = wrapper
	if code, err := (&RsyncExecutor{binary: wrapper}).RunRestore(context.Background(), fixture.restore, func(string, string) {}, nil); err != nil || code != 0 {
		t.Fatalf("restore after source mutation code=%d err=%v", code, err)
	}
	got, err := os.ReadFile(filepath.Join(fixture.target, "selected.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "captured source" {
		t.Fatalf("staged restore content=%q, want %q", got, "captured source")
	}
	mutated, err := os.ReadFile(filepath.Join(fixture.backup, "selected.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mutated) != "mutated source" {
		t.Fatalf("mutation wrapper did not run, source=%q", mutated)
	}
}
func TestStageRsyncRestorePreservesArchiveMetadata(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "special"), []byte("archive metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "special"), 0o4750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "sticky"), 0o1770); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "link")
	if err := os.Symlink("special", link); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	captureTask := model.Task{ExecutorType: "rsync", RsyncSource: source + string(filepath.Separator), RsyncTarget: backup, RsyncBinary: rsyncBinary}
	raw, err := CaptureRsyncManifest(context.Background(), captureTask)
	if err != nil {
		t.Fatalf("capture metadata fixture: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), captureTask, func(string, string) {}, nil); err != nil || code != 0 {
		t.Fatalf("seed metadata fixture code=%d err=%v", code, err)
	}
	special := filepath.Join(backup, "special")
	sticky := filepath.Join(backup, "sticky")
	link = filepath.Join(backup, "link")
	if err := os.Chmod(special, 0o4750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, 0o1770); err != nil {
		t.Fatal(err)
	}
	linkTime := time.Unix(123456789, 987654321)
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, link, []unix.Timespec{unix.NsecToTimespec(linkTime.UnixNano()), unix.NsecToTimespec(linkTime.UnixNano())}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatalf("set symlink timestamp: %v", err)
	}
	staged, cleanup, err := stageRsyncRestoreSource(context.Background(), backup, manifest)
	if err != nil {
		t.Fatalf("stage metadata fixture: %v", err)
	}
	stagedSpecial := filepath.Join(staged, "special")
	stagedSticky := filepath.Join(staged, "sticky")
	stagedLink := filepath.Join(staged, "link")
	t.Cleanup(cleanup)
	for _, tc := range []struct {
		name string
		path string
		want os.FileMode
	}{
		{name: "setuid file", path: stagedSpecial, want: 0o4750},
		{name: "sticky directory", path: stagedSticky, want: 0o1770},
	} {
		info, statErr := os.Lstat(tc.path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != tc.want&os.ModePerm || info.Mode()&os.ModeSetuid != tc.want&os.ModeSetuid || info.Mode()&os.ModeSticky != tc.want&os.ModeSticky {
			t.Fatalf("%s mode=%#o (%s), want %#o", tc.name, info.Mode().Perm(), info.Mode(), tc.want)
		}
	}
	sourceInfo, err := os.Lstat(special)
	if err != nil {
		t.Fatal(err)
	}
	stagedInfo, err := os.Lstat(stagedSpecial)
	if err != nil {
		t.Fatal(err)
	}
	sourceStat := sourceInfo.Sys().(*syscall.Stat_t)
	stagedStat := stagedInfo.Sys().(*syscall.Stat_t)
	if sourceStat.Uid != stagedStat.Uid || sourceStat.Gid != stagedStat.Gid {
		t.Fatalf("staged owner=%d:%d, want %d:%d", stagedStat.Uid, stagedStat.Gid, sourceStat.Uid, sourceStat.Gid)
	}
	stagedLinkInfo, err := os.Lstat(stagedLink)
	if err != nil {
		t.Fatal(err)
	}
	if !stagedLinkInfo.ModTime().Equal(linkTime) {
		t.Fatalf("staged symlink mtime=%s, want %s", stagedLinkInfo.ModTime(), linkTime)
	}
	if err := os.Chmod(filepath.Clean(staged), 0o500); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Lstat(staged); !os.IsNotExist(err) {
		t.Fatalf("staging cleanup err=%v", err)
	}
}

func TestRsyncRestoreSingleFileTargetShapes(t *testing.T) {
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

	for _, rootShape := range []struct {
		name        string
		backupIsDir bool
	}{
		{name: "empty-root", backupIsDir: false},
		{name: "nonempty-root", backupIsDir: true},
	} {
		t.Run(rootShape.name, func(t *testing.T) {
			fixtureRoot := t.TempDir()
			source := filepath.Join(fixtureRoot, "selected.txt")
			backup := filepath.Join(fixtureRoot, "backup")
			if err := os.WriteFile(source, []byte("captured source"), 0o640); err != nil {
				t.Fatal(err)
			}
			if rootShape.backupIsDir {
				if err := os.Mkdir(backup, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			captureTask := model.Task{
				ExecutorType: "rsync",
				RsyncSource:  source,
				RsyncTarget:  backup,
				RsyncBinary:  rsyncBinary,
			}
			rawManifest, err := CaptureRsyncManifest(context.Background(), captureTask)
			if err != nil {
				t.Fatalf("capture manifest: %v", err)
			}
			manifest, err := model.DecodeRsyncCaptureManifest(rawManifest)
			if err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
			if manifest.Layout != model.TaskRunCaptureLayoutSingleFile {
				t.Fatalf("layout=%q, want single-file", manifest.Layout)
			}
			if rootShape.backupIsDir && manifest.Root == "" {
				t.Fatal("directory backup produced empty capture root")
			}
			if !rootShape.backupIsDir && manifest.Root != "" {
				t.Fatalf("file backup produced capture root %q", manifest.Root)
			}
			if code, err := (&RsyncExecutor{binary: rsyncBinary}).Run(context.Background(), captureTask, func(string, string) {}, nil); err != nil || code != 0 {
				t.Fatalf("seed backup code=%d err=%v", code, err)
			}

			for _, targetShape := range []struct {
				name          string
				existingKind  string
				trailingSlash bool
			}{
				{name: "absent-file"},
				{name: "existing-file", existingKind: "file"},
				{name: "absent-directory", trailingSlash: true},
				{name: "existing-directory", existingKind: "directory"},
			} {
				t.Run(targetShape.name, func(t *testing.T) {
					target := filepath.Join(fixtureRoot, "target-"+targetShape.name)
					if targetShape.trailingSlash {
						target += string(filepath.Separator)
					}
					switch targetShape.existingKind {
					case "file":
						if err := os.WriteFile(filepath.Clean(target), []byte("old target"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "directory":
						if err := os.Mkdir(filepath.Clean(target), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					restoreTask := captureTask
					restoreTask.Node = node
					restoreTask.RsyncSource = backup
					restoreTask.RsyncTarget = target
					restoreTask.RsyncCaptureLayout = manifest.Layout
					restoreTask.RsyncCaptureRoot = manifest.Root
					restoreTask.RsyncCaptureManifest = rawManifest
					if code, err := (&RsyncExecutor{binary: rsyncBinary}).RunRestore(context.Background(), restoreTask, func(string, string) {}, nil); err != nil || code != 0 {
						t.Fatalf("restore code=%d err=%v", code, err)
					}
					restored := filepath.Clean(target)
					if targetShape.trailingSlash || targetShape.existingKind == "directory" {
						restoredName := filepath.Base(backup)
						if manifest.Root != "" {
							restoredName = manifest.Root
						}
						restored = filepath.Join(restored, restoredName)
					}
					got, err := os.ReadFile(restored)
					if err != nil {
						t.Fatalf("read restored path %q: %v", restored, err)
					}
					if string(got) != "captured source" {
						t.Fatalf("restored content=%q, want %q", got, "captured source")
					}
				})
			}
		})
	}
}
