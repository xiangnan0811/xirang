package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/testutil"
)

func TestResticRunCancellationCleansPasswordViaIndependentSSH(t *testing.T) {
	testResticLegacyCancellationCleansPassword(t, false)
}

func TestResticRestoreCancellationCleansPasswordViaIndependentSSH(t *testing.T) {
	testResticLegacyCancellationCleansPassword(t, true)
}

func testResticLegacyCancellationCleansPassword(t *testing.T, restore bool) {
	t.Helper()
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)

	markerDir := t.TempDir()
	resticBinary := filepath.Join(t.TempDir(), "restic-noncooperative")
	writeNonCooperativeRestic(t, resticBinary, markerDir)
	repo := filepath.Join(t.TempDir(), "repository")
	target := filepath.Join(t.TempDir(), "restore-target")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	task := model.Task{
		ExecutorType:   "restic",
		RsyncSource:    filepath.Join(t.TempDir(), "source"),
		RsyncTarget:    repo,
		ExecutorConfig: `{"repository_password":"FAKE_RESTIC_CLEANUP_PASSWORD_FOR_TEST_ONLY"}`,
		Node:           node,
	}
	if restore {
		task.RsyncSource = repo
		task.RsyncTarget = target
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runResult struct {
		exitCode int
		err      error
	}
	resultCh := make(chan runResult, 1)
	runner := &ResticExecutor{binary: resticBinary}
	go func() {
		var exitCode int
		var runErr error
		if restore {
			exitCode, runErr = runner.RunRestore(ctx, task, func(string, string) {}, nil)
		} else {
			exitCode, runErr = runner.Run(ctx, task, func(string, string) {}, nil)
		}
		resultCh <- runResult{exitCode: exitCode, err: runErr}
	}()

	operation := "backup"
	if restore {
		operation = "restore"
	}
	passwordPath := waitForResticPasswordMarker(t, filepath.Join(markerDir, operation))
	passwordContents, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatalf("read published fake password: %v", err)
	}
	if string(passwordContents) != "FAKE_RESTIC_CLEANUP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("published password=%q", passwordContents)
	}
	passwordInfo, err := os.Stat(passwordPath)
	if err != nil {
		t.Fatalf("stat published password: %v", err)
	}
	if got := passwordInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("published password mode=%04o, want 0600", got)
	}
	passwordDir := filepath.Dir(passwordPath)
	dirInfo, err := os.Stat(passwordDir)
	if err != nil {
		t.Fatalf("stat published password directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("published password directory mode=%04o, want 0700", got)
	}

	cancel()
	select {
	case result := <-resultCh:
		if result.exitCode != -1 {
			t.Fatalf("canceled %s exit=%d, want -1 (err=%v)", operation, result.exitCode, result.err)
		}
		if result.err == nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled %s error=%v, want context cancellation", operation, result.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("canceled %s did not join bounded operation lifecycle", operation)
	}
	if _, err := os.Lstat(passwordPath); !os.IsNotExist(err) {
		t.Fatalf("password path after independent cleanup err=%v", err)
	}
	if _, err := os.Lstat(passwordDir); !os.IsNotExist(err) {
		t.Fatalf("password directory after independent cleanup err=%v", err)
	}
}

func writeNonCooperativeRestic(t *testing.T, binary, markerDir string) {
	t.Helper()
	markerDirArg := ShellEscape(markerDir)
	script := fmt.Sprintf(`#!/bin/sh
marker_dir=%s
password_file=
operation=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --password-file)
      password_file=$2
      shift 2
      continue
      ;;
    backup|restore|snapshots|ls)
      operation=$1
      ;;
  esac
  shift
done
if [ -n "$operation" ] && [ -n "$password_file" ] && [ -f "$password_file" ]; then
  printf '%%s\n' "$password_file" > "$marker_dir/$operation"
fi
case "$operation" in
  snapshots)
    printf '[]\n'
    ;;
  backup|restore|ls)
    printf '{"message_type":"status","total_bytes":1,"bytes_done":0}\n'
    trap '' TERM INT HUP
    while :; do sleep 1; done
    ;;
esac
`, markerDirArg)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write non-cooperative Restic: %v", err)
	}
}

func waitForResticPasswordMarker(t *testing.T, markerPath string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(markerPath)
		if err == nil && strings.TrimSpace(string(contents)) != "" {
			return strings.TrimSpace(string(contents))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Restic operation did not publish password marker %q", markerPath)
	return ""
}
