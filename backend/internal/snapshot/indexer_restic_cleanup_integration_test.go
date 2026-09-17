package snapshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"
)

func TestLegacyResticIndexerCancellationCleansPasswordViaIndependentSSH(t *testing.T) {
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)

	markerDir := t.TempDir()
	resticBinary := filepath.Join(t.TempDir(), "restic-noncooperative")
	writeNonCooperativeIndexerRestic(t, resticBinary, markerDir)
	t.Setenv("RESTIC_BINARY", resticBinary)

	db := openIndexerTestDB(t)
	taskEntity := model.Task{
		ID:             1,
		Name:           "restic-index-cleanup",
		ExecutorType:   "restic",
		RsyncTarget:    filepath.Join(t.TempDir(), "repository"),
		ExecutorConfig: `{"repository_password":"FAKE_RESTIC_INDEX_PASSWORD_FOR_TEST_ONLY"}`,
		Node:           node,
		Status:         "pending",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- legacyIndexSnapshotWithLimits(ctx, db, taskEntity, indexerPointOne, resticLSLimits{
			timeout:        30 * time.Second,
			maxOutputBytes: 1 << 20,
			maxRecordBytes: 1 << 20,
			maxEntries:     1000,
			maxStderrBytes: 64 << 10,
		})
	}()

	passwordPath := waitForIndexerResticPasswordMarker(t, filepath.Join(markerDir, "ls"), resultCh)
	passwordContents, err := os.ReadFile(passwordPath)
	if err != nil {
		t.Fatalf("read published fake password: %v", err)
	}
	if string(passwordContents) != "FAKE_RESTIC_INDEX_PASSWORD_FOR_TEST_ONLY" {
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
		if result == nil {
			t.Fatal("canceled Restic index unexpectedly succeeded")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled Restic index did not join bounded operation lifecycle")
	}
	if _, err := os.Lstat(passwordPath); !os.IsNotExist(err) {
		t.Fatalf("password path after independent cleanup err=%v", err)
	}
	if _, err := os.Lstat(passwordDir); !os.IsNotExist(err) {
		t.Fatalf("password directory after independent cleanup err=%v", err)
	}
}

func writeNonCooperativeIndexerRestic(t *testing.T, binary, markerDir string) {
	t.Helper()
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
    ls)
      operation=ls
      ;;
  esac
  shift
done
if [ "$operation" = ls ] && [ -n "$password_file" ] && [ -f "$password_file" ]; then
  printf '%%s\n' "$password_file" > "$marker_dir/ls"
  trap '' TERM INT HUP
  while :; do sleep 1; done
fi
`, executor.ShellEscape(markerDir))
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatalf("write non-cooperative Restic: %v", err)
	}
}

func waitForIndexerResticPasswordMarker(t *testing.T, markerPath string, resultCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(markerPath)
		if err == nil && strings.TrimSpace(string(contents)) != "" {
			return strings.TrimSpace(string(contents))
		}
		select {
		case result := <-resultCh:
			t.Fatalf("Restic index returned before publishing password marker: %v", result)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Restic index did not publish password marker %q", markerPath)
	return ""
}
