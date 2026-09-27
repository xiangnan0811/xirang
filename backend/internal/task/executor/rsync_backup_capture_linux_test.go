//go:build linux

package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	capture, err := PrepareRsyncBackupCapture(context.Background(), task)
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
