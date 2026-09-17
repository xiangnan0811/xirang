package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/testutil"
)

func TestLegacyRcloneExecutorRejectsManagedModeBeforeSSH(t *testing.T) {
	executor := &RcloneExecutor{binary: "rclone"}
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			taskEntity := model.Task{
				ID: 7, ExecutorType: "rclone", RsyncSource: "/srv/source", RsyncTarget: "legacy:bucket/path",
				ExecutorConfig: `{"version":1,"publication_mode":"` + string(mode) + `","transfers":4}`,
				Node:           model.Node{ID: 9, Host: "127.0.0.1", Port: 1, Username: "reader", AuthType: "password"},
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if exitCode, err := executor.Run(ctx, taskEntity, func(string, string) {}, nil); exitCode != -1 || !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("legacy managed backup exit=%d err=%v", exitCode, err)
			}
			if exitCode, err := executor.RunRestore(ctx, taskEntity, func(string, string) {}, nil); exitCode != -1 || !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("legacy managed restore exit=%d err=%v", exitCode, err)
			}
		})
	}
}

func TestLegacyRcloneExecutorStreamsAndCompletesKnownExit(t *testing.T) {
	node := startRcloneSSHTestServer(t)
	binary := writeRcloneScript(t, "printf 'Transferred: 1 MiB / 1 MiB, 100%%, 1 MiB/s, ETA 0s\\n'\nexit 0\n")
	var logs []string
	executor := &RcloneExecutor{binary: binary}
	taskEntity := model.Task{
		ExecutorType: "rclone", RsyncSource: "/srv/source", RsyncTarget: "legacy:bucket/path",
		Node: node,
	}
	exitCode, err := executor.Run(context.Background(), taskEntity, func(_, message string) { logs = append(logs, message) }, nil)
	if err != nil || exitCode != 0 {
		t.Fatalf("known exit=%d err=%v logs=%v", exitCode, err, logs)
	}
	if len(logs) < 2 {
		t.Fatalf("stream logs=%v, want start and provider output", logs)
	}
}

func TestLegacyRcloneExecutorCancellationClosesScannerAndWait(t *testing.T) {
	node := startRcloneSSHTestServer(t)
	binary := writeRcloneScript(t, "trap '' TERM\nwhile :; do printf 'Transferred: 1 MiB / 1 MiB, 50%%, 1 MiB/s, ETA 1s\\n'; sleep 0.01; done\n")
	executor := &RcloneExecutor{binary: binary}
	taskEntity := model.Task{ExecutorType: "rclone", RsyncSource: "/srv/source", RsyncTarget: "legacy:bucket/path", Node: node}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, taskEntity, func(_, message string) {
			if !strings.HasPrefix(message, "Transferred:") {
				return
			}
			select {
			case <-started:
			default:
				close(started)
			}
		}, nil)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("rclone stream did not start")
	}
	cancel()
	select {
	case err := <-result:
		var unknown *RemoteExecutionUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("cancellation error=%v, want RemoteExecutionUnknownError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rclone cancellation exceeded bounded lifecycle")
	}
}

func TestLegacyRcloneExecutorNoStartIsTyped(t *testing.T) {
	executor := &RcloneExecutor{binary: "rclone"}
	_, err := executor.Run(context.Background(), model.Task{ExecutorType: "rclone", ExecutorConfig: "{"}, func(string, string) {}, nil)
	var noStart *NoProcessStartError
	if !errors.As(err, &noStart) {
		t.Fatalf("parse error=%v, want NoProcessStartError", err)
	}
}

func startRcloneSSHTestServer(t *testing.T) model.Node {
	t.Helper()
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	return testutil.StartRsyncSSHServer(t, sshdBinary)
}

func writeRcloneScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rclone-fake")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
