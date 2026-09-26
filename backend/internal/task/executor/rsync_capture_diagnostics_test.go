package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/model"
)

func TestRsyncCaptureCommandFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		diagnostic string
	}{
		{"vanished", 24, "file has vanished: /data/part"},
		{"permission", 23, "rsync: open /data/part failed: Permission denied (13)"},
		{"space", 11, "rsync: write failed: No space left on device (28)"},
	} {
		for _, phase := range []string{"copy", "selection"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				binary := filepath.Join(t.TempDir(), "rsync")
				script := fmt.Sprintf("#!/bin/sh\ncat >&2 <<'DIAGNOSTIC'\n%s\nDIAGNOSTIC\nexit %d\n", tc.diagnostic, tc.code)
				if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
				task := model.Task{RsyncBinary: binary, RsyncSource: t.TempDir() + "/"}
				var err error
				if phase == "copy" {
					_, err = copyRsyncCaptureSelection(context.Background(), task, task.RsyncSource, nil, RsyncCaptureSourceRole)
				} else {
					_, err = listRsyncCaptureEntries(context.Background(), task, task.RsyncSource, nil, RsyncCaptureSourceRole)
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.code {
					t.Fatalf("lost exit error: %v", err)
				}
				for _, want := range []string{tc.diagnostic, fmt.Sprintf("exit status %d", tc.code)} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("missing %q in %v", want, err)
					}
				}
			})
		}
	}
}

func TestRsyncCaptureFailurePreservesStartAndContextErrors(t *testing.T) {
	task := model.Task{RsyncBinary: filepath.Join(t.TempDir(), "missing-rsync"), RsyncSource: t.TempDir() + "/"}
	_, err := copyRsyncCaptureSelection(context.Background(), task, task.RsyncSource, nil, RsyncCaptureSourceRole)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("lost start failure: %v", err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		got := newRsyncCaptureFailure("rsync capture evidence copy failed", cause)
		if !errors.Is(got, cause) || !strings.Contains(got.Error(), cause.Error()) {
			t.Fatalf("lost context failure: %v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task.RsyncBinary = "/bin/sh"
	_, err = copyRsyncCaptureSelection(ctx, task, task.RsyncSource, nil, RsyncCaptureSourceRole)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost command cancellation: %v", err)
	}
}

type captureCancelWriter struct{ cancel context.CancelFunc }

func (w captureCancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestRsyncCaptureRunningCommandPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runRsyncCaptureCommand(ctx, model.Task{RsyncBinary: "/bin/sh"},
		[]string{"-c", "printf ready >&2; exec sleep 60"}, "", "", false, false, nil,
		io.Discard, captureCancelWriter{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation after process start: %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("lost killed process error: %v", err)
	}
}

func TestRsyncCaptureFailureBoundsAndSanitizesDiagnostics(t *testing.T) {
	for _, closing := range []string{"\n-----END OPENSSH PRIVATE KEY-----", ""} {
		t.Run(fmt.Sprintf("complete=%t", closing != ""), func(t *testing.T) {
			stderr := &rsyncCaptureOutputBuffer{limit: 1024}
			_, _ = stderr.Write([]byte("Permission denied; token=FAKE_CAPTURE_TOKEN_FOR_TEST_ONLY\nhttps://user:FAKE_URL_PASSWORD_FOR_TEST_ONLY@example.test/?token=FAKE_QUERY_FOR_TEST_ONLY\n-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE_PRIVATE_KEY_BODY_FOR_TEST_ONLY" + closing))
			stdout := &rsyncCaptureOutputBuffer{limit: 8}
			_, _ = stdout.Write([]byte("FAKE_FILE_LIST_FOR_TEST_ONLY"))
			got := newRsyncCaptureFailure("rsync capture evidence copy failed", errors.New("exit status 23"), stderr, stdout).Error()
			for _, forbidden := range []string{"FAKE_", "BEGIN", "END OPENSSH", "example.test"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("leaked %q: %s", forbidden, got)
				}
			}
			for _, want := range []string{"Permission denied", "exit status 23", "exceeded 8 bytes"} {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q: %s", want, got)
				}
			}
		})
	}
	stderr := &rsyncCaptureOutputBuffer{limit: 1024}
	_, _ = stderr.Write([]byte("No space left on device " + strings.Repeat("x", 2000)))
	got := newRsyncCaptureFailure("rsync capture evidence copy failed", errors.New("exit status 11"), stderr).Error()
	if len([]rune(got)) > 501 || !strings.Contains(got, "exceeded 1024 bytes") || !strings.Contains(got, "No space left on device") {
		t.Fatalf("bad bounded diagnostic: %s", got)
	}
}
