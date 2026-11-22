//go:build linux

package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
	"xirang/backend/internal/task/testutil"
)

func TestRsyncBackupCaptureConfinedRemoteSource(t *testing.T) {
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skip("RSYNC_CONFINEMENT_HELPER required for confinement integration")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Fatal(err)
	}
	node := testutil.StartRsyncSSHServer(t, sshd)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv(rsyncconfinement.RemoteHelperPathEnv, helper)
	sourceRoot, targetRoot := t.TempDir(), t.TempDir()
	source := filepath.Join(sourceRoot, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	backupCaptureWrite(t, filepath.Join(source, "payload"), "remote captured bytes")
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, sourceRoot)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
	task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: filepath.Join(targetRoot, "backup"), RsyncBinary: backupCaptureBinary(t), Node: node}
	capture, err := PrepareRsyncBackupCapture(context.Background(), task, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capture.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if code, err := capture.Run(context.Background(), task, func(string, string) {}, nil); code != 0 || err != nil {
		t.Fatalf("stable confined transfer=%d %v", code, err)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, capture.Manifest()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(task.RsyncTarget, "payload"))
	if err != nil || string(got) != "remote captured bytes" {
		t.Fatalf("payload=%q %v", got, err)
	}
	// The next run uses the confined target as a read-only delta basis.
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	backupCaptureWrite(t, filepath.Join(source, "payload"), "remote captured bytes, revised")
	backupCaptureWrite(t, filepath.Join(source, "added"), "added bytes")
	next, err := PrepareRsyncBackupCapture(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("confined capture with basis: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(task.RsyncTarget, "payload"))
	if err != nil || string(got) != "remote captured bytes" {
		t.Fatalf("basis modified before transfer=%q %v", got, err)
	}
	if code, err := next.Run(context.Background(), task, func(string, string) {}, nil); code != 0 || err != nil {
		t.Fatalf("confined transfer from basis capture=%d %v", code, err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, next.Manifest()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"payload": "remote captured bytes, revised", "added": "added bytes"} {
		if got, err := os.ReadFile(filepath.Join(task.RsyncTarget, name)); err != nil || string(got) != want {
			t.Fatalf("%s after basis capture=%q %v", name, got, err)
		}
	}
	// An owned staging source must not widen target capabilities. Replace the
	// original target with a symlink to a different tree before another attempt.
	outside := t.TempDir()
	backupCaptureWrite(t, filepath.Join(outside, "payload"), "must remain untouched")
	if err := os.RemoveAll(task.RsyncTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, task.RsyncTarget); err != nil {
		t.Fatal(err)
	}
	if code, err := capture.Run(context.Background(), task, func(string, string) {}, nil); code == 0 || err == nil {
		t.Fatal("target confinement escaped through alias")
	}
	got, err = os.ReadFile(filepath.Join(outside, "payload"))
	if err != nil || string(got) != "must remain untouched" {
		t.Fatalf("outside target changed=%q %v", got, err)
	}
}

func TestRsyncBackupCaptureRemoteRestrictiveModes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the staged file read permission")
	}
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}

	fixtureBin := t.TempDir()
	marker := filepath.Join(fixtureBin, "sender-used")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// The real source stays accessible; only the sender's advertised owner
	// read permissions are removed before the receiving capture adjusts them.
	sender := "#!/bin/sh\n: > " + quote(marker) + "\nexec " + quote(rsync) + " --chmod=Du-r,Fu-r \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fixtureBin, "rsync"), []byte(sender), 0700); err != nil {
		t.Fatal(err)
	}
	sshdWrapper := filepath.Join(fixtureBin, "sshd")
	server := "#!/bin/sh\nexec " + quote(sshd) + " -o " + quote("SetEnv=PATH="+fixtureBin+":/usr/bin:/bin") + " \"$@\"\n"
	if err := os.WriteFile(sshdWrapper, []byte(server), 0700); err != nil {
		t.Fatal(err)
	}
	node := testutil.StartRsyncSSHServer(t, sshdWrapper)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")

	source := t.TempDir()
	sourceDir := filepath.Join(source, "nested")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDir, 0731); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(sourceDir, "payload")
	backupCaptureWrite(t, sourceFile, "abc")
	if err := os.Chmod(sourceFile, 0640); err != nil {
		t.Fatal(err)
	}
	sourceExists, sourceMode, sourceTime, sourceData := snapshotRsyncPath(t, sourceFile)
	dirExists, dirMode, dirTime, dirData := snapshotRsyncPath(t, sourceDir)
	target := filepath.Join(t.TempDir(), "backup")
	task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: rsync, Node: node}
	ctx := context.Background()
	capture, err := PrepareRsyncBackupCapture(ctx, task, nil)
	if err != nil {
		t.Fatalf("capture remote source with restrictive sender modes: %v", err)
	}
	t.Cleanup(func() {
		if err := capture.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("remote sender fixture was not used: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(capture.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range manifest.Entries {
		if entry.Path == "nested/payload" {
			found = true
			if entry.Kind != "file" || entry.Size != 3 || entry.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
				t.Fatalf("payload capture evidence: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("payload missing from capture: %+v", manifest.Entries)
	}
	assertRsyncPathSnapshot(t, sourceFile, sourceExists, sourceMode, sourceTime, sourceData)
	assertRsyncPathSnapshot(t, sourceDir, dirExists, dirMode, dirTime, dirData)
	// Changing the live source after capture must not affect the local copy.
	backupCaptureWrite(t, sourceFile, "changed")
	sourceExists, sourceMode, sourceTime, sourceData = snapshotRsyncPath(t, sourceFile)
	if code, err := capture.Run(ctx, task, func(string, string) {}, nil); code != 0 || err != nil {
		t.Fatalf("stable local transfer=%d %v", code, err)
	}
	if err := VerifyRsyncCaptureManifestTarget(ctx, task, capture.Manifest()); err != nil {
		t.Fatalf("verify captured target: %v", err)
	}
	targetDir := filepath.Join(target, "nested")
	targetFile := filepath.Join(targetDir, "payload")
	got, err := os.ReadFile(targetFile)
	if err != nil || string(got) != "abc" {
		t.Fatalf("target payload=%q err=%v", got, err)
	}
	assertModesAndSource := func(content string) {
		t.Helper()
		for _, tc := range []struct {
			path string
			want os.FileMode
		}{
			{sourceDir, 0731}, {targetDir, 0731},
			{sourceFile, 0640}, {targetFile, 0640},
		} {
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.want {
				t.Errorf("%s mode=%04o, want %04o", tc.path, info.Mode().Perm(), tc.want)
			}
		}
		if got, err := os.ReadFile(sourceFile); err != nil || string(got) != content {
			t.Fatalf("source payload=%q err=%v, want %q", got, err, content)
		}
	}
	assertModesAndSource("changed")
	assertRsyncPathSnapshot(t, sourceFile, sourceExists, sourceMode, sourceTime, sourceData)
	assertRsyncPathSnapshot(t, sourceDir, dirExists, dirMode, dirTime, dirData)
	// An unreadable previous target must disable the basis optimization, not
	// prevent a fresh capture or change the live source.
	if err := os.Chmod(targetFile, 0200); err != nil {
		t.Fatal(err)
	}
	var warned bool
	next, err := PrepareRsyncBackupCapture(ctx, task, func(level, message string) {
		if level == "warn" && strings.Contains(message, "增量基准准备失败，本次改为全量捕获") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("capture with unreadable basis: %v", err)
	}
	t.Cleanup(func() {
		if err := next.Close(); err != nil {
			t.Error(err)
		}
	})
	if !warned {
		t.Fatal("missing warning about falling back to full capture")
	}
	if code, err := next.Run(ctx, task, func(string, string) {}, nil); code != 0 || err != nil {
		t.Fatalf("full transfer after unreadable basis=%d %v", code, err)
	}
	if err := VerifyRsyncCaptureManifestTarget(ctx, task, next.Manifest()); err != nil {
		t.Fatalf("verify second captured target: %v", err)
	}
	if got, err := os.ReadFile(targetFile); err != nil || string(got) != "changed" {
		t.Fatalf("second target payload=%q err=%v", got, err)
	}
	assertModesAndSource("changed")
	assertRsyncPathSnapshot(t, sourceFile, sourceExists, sourceMode, sourceTime, sourceData)
	assertRsyncPathSnapshot(t, sourceDir, dirExists, dirMode, dirTime, dirData)
}
