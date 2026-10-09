package cronbackup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerReviewExitedLeaderHoldingStdoutDoesNotWaitForEOF(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	childPath := filepath.Join(output, "child.pid")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf 'helper backup' > \"$output/"+testArtifactSQLite+"\"\n(sleep 60) &\nchild=$!\nprintf '%s\\n' \"$child\" > "+shellQuote(childPath)+"\nprintf '"+testArtifactSQLite+"\\n' >&3\n")

	started := time.Now()
	if err := Run(context.Background(), cfg, script, output); err != nil {
		t.Fatalf("Run with an exited leader and live stdout descendant = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run waited for descendant stdout EOF: %v", elapsed)
	}
	childPID := runnerReviewReadPID(t, childPath)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusSuccess {
		t.Fatalf("exited leader observation = %+v, %v", observation, err)
	}
}

func TestRunnerReviewLateCancellationWithValidReceiptSucceeds(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	pidPath := filepath.Join(output, "leader.pid")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf '%s\\n' \"$$\" > "+shellQuote(pidPath)+"\nprintf 'helper backup' > \"$output/"+testArtifactSQLite+"\"\nprintf '"+testArtifactSQLite+"\\n' >&3\ntrap 'exit 0' TERM INT\ntouch "+shellQuote(ready)+"\nwhile :; do sleep 1; done\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		if pid, err := os.ReadFile(pidPath); err == nil {
			if value, parseErr := strconv.Atoi(strings.TrimSpace(string(pid))); parseErr == nil {
				killRunnerReviewGroup(value)
			}
		}
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(ctx, cfg, script, output) }()
	waitForFile(t, ready)
	cancel()
	var runErr error
	select {
	case runErr = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("late-cancel run did not finish")
	}
	if runErr != nil {
		t.Fatalf("late cancellation with a valid receipt = %v", runErr)
	}

	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusSuccess || observation.Job.LastSuccess == nil {
		t.Fatalf("late-cancel observation = %+v, %v", observation, err)
	}
}

func TestRunnerReviewCancellationDuringReceiptReadCleansSameGroupDescendant(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	leaderPIDPath := filepath.Join(output, "leader.pid")
	descendantPIDPath := filepath.Join(output, "descendant.pid")
	terminatedPath := filepath.Join(output, "descendant-terminated")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\n"+
		"terminated="+shellQuote(terminatedPath)+"\n"+
		"printf '%s\\n' \"$$\" > "+shellQuote(leaderPIDPath)+"\n"+
		"(\n"+
		"  exec 4>&-\n"+
		"  trap 'printf terminated > \"$terminated\"; exit 0' TERM INT\n"+
		"  touch "+shellQuote(ready)+"\n"+
		"  while :; do sleep 1 || :; done\n"+
		") &\n"+
		"descendant=$!\n"+
		"printf '%s\\n' \"$descendant\" > "+shellQuote(descendantPIDPath)+"\n"+
		"while [ ! -f "+shellQuote(ready)+" ]; do sleep 0.01; done\n"+
		"printf 'helper backup' > \"$output/"+testArtifactSQLite+"\"\n"+
		"printf '%s\\n' '"+testArtifactSQLite+"' >&3\n")

	oldReceiptReadHook := receiptReadHook
	receiptReadStarted := make(chan struct{})
	releaseReceiptRead := make(chan struct{}, 1)
	receiptReadHook = func() {
		close(receiptReadStarted)
		<-releaseReceiptRead
	}
	releaseReceiptReadHook := func() {
		select {
		case releaseReceiptRead <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		releaseReceiptReadHook()
		for _, path := range []string{leaderPIDPath, descendantPIDPath} {
			if pid, err := os.ReadFile(path); err == nil {
				if value, parseErr := strconv.Atoi(strings.TrimSpace(string(pid))); parseErr == nil {
					killRunnerReviewGroup(value)
				}
			}
		}
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
		receiptReadHook = oldReceiptReadHook
	})

	go func() { result <- Run(ctx, cfg, script, output) }()
	select {
	case <-receiptReadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("receipt read hook was not reached")
	}
	descendantPID := runnerReviewReadPID(t, descendantPIDPath)
	if err := syscall.Kill(descendantPID, 0); err != nil {
		t.Fatalf("same-group descendant was not alive during receipt read: %v", err)
	}
	cancel()
	releaseReceiptReadHook()

	var runErr error
	select {
	case runErr = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("receipt-read cancellation run did not finish")
	}
	if runErr != nil {
		t.Fatalf("receipt-read cancellation with valid receipt = %v", runErr)
	}
	waitForFile(t, terminatedPath)

	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusSuccess || observation.Job.LastSuccess == nil || observation.Job.LastSuccess.ArtifactName != testArtifactSQLite {
		t.Fatalf("receipt-read cancellation observation = %+v, %v", observation, err)
	}
}

func TestRunnerReviewNormalTERMDoesNotWaitForZombieLeader(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	pidPath := filepath.Join(output, "leader.pid")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf '%s\\n' \"$$\" > "+shellQuote(pidPath)+"\ntrap 'exit 1' TERM INT\ntouch "+shellQuote(ready)+"\nwhile :; do sleep 1; done\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		if pid, err := os.ReadFile(pidPath); err == nil {
			if value, parseErr := strconv.Atoi(strings.TrimSpace(string(pid))); parseErr == nil {
				killRunnerReviewGroup(value)
			}
		}
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(ctx, cfg, script, output) }()
	waitForFile(t, ready)
	started := time.Now()
	cancel()
	var runErr error
	select {
	case runErr = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("normal TERM run did not finish")
	}
	if !errors.Is(runErr, ErrProcessInterrupted) {
		t.Fatalf("normal TERM result = %v", runErr)
	}
	if elapsed := time.Since(started); elapsed >= groupTerminationGrace {
		t.Fatalf("normal TERM waited for the zombie grace period: %v", elapsed)
	}
}

func TestRunnerReviewTERMIgnoringChildIsKilled(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	pidPath := filepath.Join(output, "leader.pid")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf '%s\\n' \"$$\" > "+shellQuote(pidPath)+"\ntrap '' TERM INT\ntouch "+shellQuote(ready)+"\nwhile :; do sleep 1; done\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		if pid, err := os.ReadFile(pidPath); err == nil {
			if value, parseErr := strconv.Atoi(strings.TrimSpace(string(pid))); parseErr == nil {
				killRunnerReviewGroup(value)
			}
		}
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(ctx, cfg, script, output) }()
	waitForFile(t, ready)
	started := time.Now()
	cancel()
	var runErr error
	select {
	case runErr = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("TERM-ignoring run did not finish")
	}
	if !errors.Is(runErr, ErrProcessInterrupted) {
		t.Fatalf("TERM-ignoring result = %v", runErr)
	}
	elapsed := time.Since(started)
	if elapsed < groupTerminationGrace || elapsed > groupTerminationGrace+groupTerminationKillGrace+3*time.Second {
		t.Fatalf("TERM-ignoring cleanup duration = %v", elapsed)
	}
}

func TestRunnerReviewEscapedDescendantFD4PreventsTerminalRecord(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	leaderPIDPath := filepath.Join(output, "leader.pid")
	escapedPIDPath := filepath.Join(output, "escaped.pid")
	inner := "setsid sh -c 'printf \"%s\\n\" \"$$\" > \"$1\"; sleep 60' sh \"$output/escaped.pid\" >/dev/null 2>&1 &\n"
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf '%s\\n' \"$$\" > "+shellQuote(leaderPIDPath)+"\nprintf 'helper backup' > \"$output/"+testArtifactSQLite+"\"\nprintf '"+testArtifactSQLite+"\\n' >&3\ntrap 'exit 0' TERM INT\n"+inner+"touch "+shellQuote(ready)+"\nwhile :; do sleep 1; done\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		for _, path := range []string{leaderPIDPath, escapedPIDPath} {
			if pid, err := os.ReadFile(path); err == nil {
				if value, parseErr := strconv.Atoi(strings.TrimSpace(string(pid))); parseErr == nil {
					killRunnerReviewGroup(value)
				}
			}
		}
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(ctx, cfg, script, output) }()
	waitForFile(t, ready)
	waitForFile(t, escapedPIDPath)
	cancel()
	var runErr error
	select {
	case runErr = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("escaped-descendant run did not finish")
	}
	if !errors.Is(runErr, ErrProcessInterrupted) {
		t.Fatalf("escaped-descendant result = %v", runErr)
	}

	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusRunning {
		t.Fatalf("escaped-descendant state = %+v, %v", observation, err)
	}
}

func TestRunnerReviewPublicationBlockedOverOneSecondSucceeds(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setRunnerReviewClock(t, initialized.Add(time.Second))

	output := t.TempDir()
	ready := filepath.Join(output, "ready")
	release := filepath.Join(output, "release")
	script := writeRunnerReviewScript(t, "#!/bin/sh\nset -eu\noutput=\"$1\"\nprintf 'helper backup' > \"$output/"+testArtifactSQLite+"\"\nprintf '"+testArtifactSQLite+"\\n' >&3\ntouch "+shellQuote(ready)+"\nwhile [ ! -f "+shellQuote(release)+" ]; do sleep 0.01; done\n")

	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release\n"), 0o600)
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(context.Background(), cfg, script, output) }()
	waitForFile(t, ready)

	storage, err := openExistingStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		storage.close()
		t.Fatal(err)
	}
	if err := acquireLock(context.Background(), lockFile, 0, ErrStateUnavailable); err != nil {
		_ = lockFile.Close()
		storage.close()
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		_ = lockFile.Close()
		storage.close()
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	_ = lockFile.Close()
	storage.close()

	select {
	case err = <-result:
		runFinished = true
	case <-time.After(10 * time.Second):
		t.Fatal("publication-blocked run did not finish")
	}
	if err != nil {
		t.Fatalf("publication blocked over one second = %v", err)
	}
	observation, readErr := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if readErr != nil || observation.Job.Status != JobStatusSuccess {
		t.Fatalf("publication-blocked state = %+v, %v", observation, readErr)
	}
}

func setRunnerReviewClock(t *testing.T, now time.Time) {
	t.Helper()
	old := runnerNow
	runnerNow = func() time.Time { return now }
	t.Cleanup(func() { runnerNow = old })
}

func writeRunnerReviewScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner-review.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func runnerReviewReadPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid pid %q: %v", data, err)
	}
	return pid
}

func killRunnerReviewGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
