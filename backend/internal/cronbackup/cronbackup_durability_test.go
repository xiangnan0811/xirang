package cronbackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	orphanRunnerHelperEnv = "XIRANG_CRONBACKUP_ORPHAN_HELPER"
	orphanStateEnv        = "XIRANG_CRONBACKUP_ORPHAN_STATE"
	orphanScriptEnv       = "XIRANG_CRONBACKUP_ORPHAN_SCRIPT"
	orphanOutputEnv       = "XIRANG_CRONBACKUP_ORPHAN_OUTPUT"
)

// TestCronBackupOrphanRunnerHelper is launched in a subprocess by
// TestCronBackupRunLockSurvivesRunnerSIGKILL. Its Run call must remain in-flight when
// the parent kills this process, leaving the shell descendant as the only
// holder of FD4.
func TestCronBackupOrphanRunnerHelper(t *testing.T) {
	if os.Getenv(orphanRunnerHelperEnv) != "1" {
		return
	}
	stateDirectory := os.Getenv(orphanStateEnv)
	scriptPath := os.Getenv(orphanScriptEnv)
	outputDirectory := os.Getenv(orphanOutputEnv)
	if stateDirectory == "" || scriptPath == "" || outputDirectory == "" {
		t.Fatal("orphan helper environment is incomplete")
	}
	runnerNow = func() time.Time {
		return time.Date(2026, 10, 9, 1, 0, 1, 0, time.UTC)
	}
	cfg := Config{StateDirectory: stateDirectory, Engine: "sqlite", MaxAge: 26 * time.Hour}
	if err := Run(context.Background(), cfg, scriptPath, outputDirectory); err != nil {
		t.Fatalf("orphan helper Run returned before parent kill: %v", err)
	}
}

func TestCronBackupRunLockSurvivesRunnerSIGKILL(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	oldClock := runnerNow
	runnerNow = func() time.Time { return initialized.Add(time.Second) }
	defer restoreRunnerClock(oldClock)

	barrierDirectory := t.TempDir()
	readyFIFO := filepath.Join(barrierDirectory, "ready.fifo")
	if err := syscall.Mkfifo(readyFIFO, 0o600); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(barrierDirectory, "shell.pid")
	scriptPath := writeOrphanLockScript(t, readyFIFO, pidPath)
	outputDirectory := t.TempDir()

	command := exec.Command(os.Args[0], "-test.run=^TestCronBackupOrphanRunnerHelper$", "-test.v")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Env = append(os.Environ(),
		orphanRunnerHelperEnv+"=1",
		orphanStateEnv+"="+cfg.StateDirectory,
		orphanScriptEnv+"="+scriptPath,
		orphanOutputEnv+"="+outputDirectory,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helperWaited := false
	shellPID := 0
	t.Cleanup(func() {
		if shellPID <= 0 {
			if pidBytes, readErr := os.ReadFile(pidPath); readErr == nil {
				shellPID, _ = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			}
		}
		if shellPID > 0 {
			killCronBackupProcessGroup(shellPID)
		}
		if command.Process != nil && !helperWaited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	readCronBackupBarrier(t, readyFIFO, "ready\n")
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read orphan shell pid: %v", err)
	}
	shellPID, err = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || shellPID <= 0 {
		t.Fatalf("invalid orphan shell pid %q: %v", pidBytes, err)
	}

	if err := command.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILLed runner exited successfully")
	}
	helperWaited = true

	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusRunning {
		t.Fatalf("state after runner SIGKILL = %+v, %v (helper output=%q/%q)", observation, err, stdout.String(), stderr.String())
	}
	if err := Run(context.Background(), cfg, "/definitely/not-started-while-locked", outputDirectory); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second runner while orphan holds FD4 = %v", err)
	}

	killCronBackupProcessGroup(shellPID)
	observation, err = Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusInterrupted {
		t.Fatalf("state after inherited lock closes = %+v, %v", observation, err)
	}
}

func writeOrphanLockScript(t *testing.T, readyFIFO, pidPath string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "orphan-lock.sh")
	body := "#!/bin/sh\nset -eu\nsleep 60 &\nchild=$!\nprintf '%s\\n' \"$$\" > " + shellQuote(pidPath) + "\nprintf 'ready\\n' > " + shellQuote(readyFIFO) + "\nwait \"$child\"\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

func killCronBackupProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func readCronBackupBarrier(t *testing.T, path, want string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	var received bytes.Buffer
	buffer := make([]byte, 128)
	for time.Now().Before(deadline) {
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = received.Write(buffer[:count])
			if bytes.Contains(received.Bytes(), []byte(want)) {
				return
			}
		}
		if readErr != nil && !errors.Is(readErr, syscall.EAGAIN) && !errors.Is(readErr, syscall.EWOULDBLOCK) && !errors.Is(readErr, io.EOF) {
			t.Fatalf("read barrier %s: %v", path, readErr)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for barrier %s, received %q", path, received.String())
}

func openCronBackupFIFOForWrite(t *testing.T, path string) *os.File {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			return file
		}
		if !errors.Is(err, syscall.ENXIO) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("open FIFO %s: %v", path, err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out opening FIFO %s for write", path)
	return nil
}

func TestCronBackupReceiptBeforeChildExit(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	oldClock := runnerNow
	runnerNow = func() time.Time { return initialized.Add(time.Second) }
	defer restoreRunnerClock(oldClock)

	barrierDirectory := t.TempDir()
	readyFIFO := filepath.Join(barrierDirectory, "receipt-ready.fifo")
	releaseFIFO := filepath.Join(barrierDirectory, "receipt-release.fifo")
	for _, path := range []string{readyFIFO, releaseFIFO} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := writeReceiptBeforeExitScript(t, readyFIFO, releaseFIFO)
	outputDirectory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	runFinished := false
	t.Cleanup(func() {
		cancel()
		if !runFinished {
			select {
			case <-result:
			case <-time.After(10 * time.Second):
			}
		}
	})
	go func() { result <- Run(ctx, cfg, scriptPath, outputDirectory) }()

	readCronBackupBarrier(t, readyFIFO, "ready\n")
	select {
	case err := <-result:
		runFinished = true
		t.Fatalf("runner returned before child-exit release: %v", err)
	default:
	}
	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusRunning {
		t.Fatalf("receipt-before-exit observation = %+v, %v", observation, err)
	}

	release := openCronBackupFIFOForWrite(t, releaseFIFO)
	if _, err := io.WriteString(release, "release\n"); err != nil {
		t.Fatal(err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	runErr := <-result
	runFinished = true
	if runErr != nil {
		t.Fatalf("released receipt runner = %v", runErr)
	}
	observation, err = Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusSuccess || observation.Job.LastSuccess == nil || observation.Job.LastSuccess.ArtifactName != testArtifactSQLite {
		t.Fatalf("receipt-before-exit final observation = %+v, %v", observation, err)
	}
}

func writeReceiptBeforeExitScript(t *testing.T, readyFIFO, releaseFIFO string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "receipt-before-exit.sh")
	body := "#!/bin/sh\nset -eu\noutput=\"$1\"\nmkdir -p \"$output\"\nprintf 'helper backup' > \"$output/" + testArtifactSQLite + "\"\nprintf '" + testArtifactSQLite + "\\n' >&3\nprintf 'ready\\n' > " + shellQuote(readyFIFO) + "\ncat " + shellQuote(releaseFIFO) + " >/dev/null\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

func TestCronBackupFinalPublishDurabilityFailures(t *testing.T) {
	cases := []string{"pre_rename_sync", "rename", "directory_sync"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
			if err := Initialize(context.Background(), cfg, initialized); err != nil {
				t.Fatal(err)
			}
			oldClock := runnerNow
			oldSyncFile := stateSyncFile
			oldSyncDirectory := stateSyncDirectory
			oldRename := stateRenameFile
			defer func() {
				runnerNow = oldClock
				stateSyncFile = oldSyncFile
				stateSyncDirectory = oldSyncDirectory
				stateRenameFile = oldRename
			}()
			runnerNow = func() time.Time { return initialized.Add(time.Second) }

			switch name {
			case "pre_rename_sync":
				regularSyncs := 0
				stateSyncFile = func(file *os.File) error {
					info, statErr := file.Stat()
					if statErr == nil && info.Mode().IsRegular() {
						regularSyncs++
						if regularSyncs == 2 {
							return errors.New("injected final state file sync failure")
						}
					}
					return oldSyncFile(file)
				}
			case "rename":
				renames := 0
				stateRenameFile = func(root *os.Root, oldName, newName string) error {
					renames++
					if renames == 2 {
						return errors.New("injected final state rename failure")
					}
					return oldRename(root, oldName, newName)
				}
			case "directory_sync":
				directorySyncs := 0
				stateSyncDirectory = func(root *os.Root) error {
					directorySyncs++
					if directorySyncs == 2 {
						return errors.New("injected final state directory sync failure")
					}
					return oldSyncDirectory(root)
				}
			}

			if err := Run(context.Background(), cfg, helperScript(t, "success", ""), t.TempDir()); !errors.Is(err, ErrStatePublishFailed) {
				t.Fatalf("Run with %s failure = %v", name, err)
			}
		})
	}
}
