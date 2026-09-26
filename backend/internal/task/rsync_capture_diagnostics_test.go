package task

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

type captureDiagnosticExecutor struct {
	successExecutor
	binary string
}

// Model a deadline or cancellation after the external command has signaled
// readiness, without racing the test against a short wall-clock deadline.
type captureStopContext struct {
	context.Context
	done   chan struct{}
	reason error
}

func (c *captureStopContext) Done() <-chan struct{} { return c.done }
func (c *captureStopContext) Err() error {
	select {
	case <-c.done:
		return c.reason
	default:
		return nil
	}
}

func TestRsyncCaptureInterruptedDiagnosticsReachTaskHistoryAndLogs(t *testing.T) {
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(reason.Error(), func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity := seedTaskForManagerTest(t, db)
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			binary := filepath.Join(dir, "rsync")
			script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = '--dry-run' ]; then exec %q "$@"; fi
done
printf 'rsync: waiting for data\n' >&2
printf ready > %q
exec sleep 60
`, rsync, ready)
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			executor := &captureDiagnosticExecutor{binary: binary}
			manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
			ctx := &captureStopContext{Context: context.Background(), done: make(chan struct{}), reason: reason}
			stop := sync.OnceFunc(func() { close(ctx.done) })
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				manager.runTaskWithContext(taskEntity.ID, runID, "manual", generateChainRunID(), ctx, nil, stop)
			}()
			defer func() { stop(); <-finished }()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case <-finished:
					t.Fatal("capture ended before command readiness")
				case <-deadline.C:
					t.Fatal("capture command did not become ready")
				case <-ticker.C:
				}
			}
			stop()
			select {
			case <-finished:
			case <-deadline.C:
				t.Fatal("capture did not stop after cancellation")
			}
			if err := manager.logDispatcher.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			var run model.TaskRun
			if err := db.First(&run, runID).Error; err != nil {
				t.Fatal(err)
			}
			if run.Status != model.TaskRunStatusCanceled || run.BackupGenerationState != "" || executor.Calls() != 0 {
				t.Fatalf("cancellation changed write boundary: %+v calls=%d", run, executor.Calls())
			}
			var logs []model.TaskLog
			if err := db.Where("task_run_id = ? AND level = ?", runID, "warn").Find(&logs).Error; err != nil {
				t.Fatal(err)
			}
			if len(logs) == 0 {
				t.Fatal("missing capture interruption diagnostic log")
			}
			messages := []string{SanitizeRuntimeEvidenceForRead(run.LastError)}
			for _, log := range logs {
				messages = append(messages, SanitizeRuntimeEvidenceForRead(log.Message))
			}
			for _, message := range messages {
				for _, want := range []string{reason.Error(), "rsync capture evidence copy failed", "rsync: waiting for data"} {
					if !strings.Contains(message, want) {
						t.Fatalf("lost interrupted capture diagnostic %q: %s", want, message)
					}
				}
			}
		})
	}
}

func (e *captureDiagnosticExecutor) RsyncBinary() string { return e.binary }

func TestRsyncCaptureDiagnosticsReachTaskHistoryAndLogs(t *testing.T) {
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Fatal(err)
	}
	db := openManagerTestDB(t)
	taskEntity := seedTaskForManagerTest(t, db)
	binary := filepath.Join(t.TempDir(), "rsync")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = '--dry-run' ]; then exec %q "$@"; fi
done
cat >&2 <<'DIAGNOSTIC'
rsync: write failed: No space left on device (28)
token=FAKE_CAPTURE_TOKEN_FOR_TEST_ONLY
-----BEGIN OPENSSH PRIVATE KEY-----
FAKE_CAPTURE_PRIVATE_KEY_FOR_TEST_ONLY
-----END OPENSSH PRIVATE KEY-----
DIAGNOSTIC
exit 11
`, rsync)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := &captureDiagnosticExecutor{binary: binary}
	manager := NewManager(db, stubExecutorFactory{executor: executor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	runID := createTestTaskRun(t, db, taskEntity.ID, "manual")
	manager.runTask(taskEntity.ID, runID, "manual", generateChainRunID())
	if err := manager.logDispatcher.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	var run model.TaskRun
	if err := db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.TaskRunStatusWarning || run.BackupGenerationState != model.TaskRunGenerationStateDirty || executor.Calls() != 1 {
		t.Fatalf("unexpected capture outcome: status=%s generation=%s calls=%d", run.Status, run.BackupGenerationState, executor.Calls())
	}
	messages := []string{run.LastError, taskEntity.LastError, SanitizeRuntimeEvidenceForRead(run.LastError)}
	var logs []model.TaskLog
	if err := db.Where("task_run_id = ? AND level = ?", runID, "warn").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("missing warning log")
	}
	for _, log := range logs {
		messages = append(messages, SanitizeRuntimeEvidenceForRead(log.Message))
	}
	for _, message := range messages {
		for _, want := range []string{"rsync capture evidence copy failed", "exit status 11", "No space left on device (28)"} {
			if !strings.Contains(message, want) {
				t.Fatalf("missing %q in user-visible evidence: %s", want, message)
			}
		}
		for _, forbidden := range []string{"FAKE_", "BEGIN OPENSSH", "END OPENSSH"} {
			if strings.Contains(message, forbidden) {
				t.Fatalf("secret in user-visible evidence: %s", message)
			}
		}
	}
}
