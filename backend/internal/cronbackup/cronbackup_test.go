package cronbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const (
	testArtifactSQLite = "xirang-sqlite-20261009-020000.db"
	testArtifactPG     = "xirang-postgres-20261009-020000.dump"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{StateDirectory: filepath.Join(t.TempDir(), "state"), Engine: "sqlite", MaxAge: 26 * time.Hour}
}

func helperScript(t *testing.T, mode, barrier string) string {
	t.Helper()
	directory := t.TempDir()
	script := filepath.Join(directory, "backup-helper.sh")
	body := "#!/bin/sh\nset -eu\noutput=\"$1\"\nmkdir -p \"$output\"\n"
	switch mode {
	case "success":
		body += "printf 'helper backup' > \"$output/" + testArtifactSQLite + "\"\nprintf '" + testArtifactSQLite + "\\n' >&3\n"
	case "failed":
		body += "exit 7\n"
	case "invalid":
		body += "printf 'not-a-backup\\n' >&3"
	case "barrier":
		body += "touch " + shellQuote(barrier) + "\nwhile :; do sleep 1; done\n"
	case "descendant":
		body += "(sleep 60) &\ntouch " + shellQuote(barrier) + "\nwait\n"
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func shellQuote(value string) string {
	return "'" + filepath.ToSlash(value) + "'"
}

func restoreRunnerClock(old func() time.Time) {
	runnerNow = old
}

func TestParseMaxAgePreservesContract(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "", want: 26 * time.Hour},
		{raw: " 1 ", want: time.Hour},
		{raw: "8760", want: 8760 * time.Hour},
	} {
		got, err := ParseMaxAge(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("ParseMaxAge(%q) = %v, %v; want %v", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{"0", "8761", "x", "1.5"} {
		if _, err := ParseMaxAge(raw); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("ParseMaxAge(%q) error = %v", raw, err)
		}
	}
}

func TestReceiptValidationIsEngineSpecific(t *testing.T) {
	if artifact, ok := validateReceipt([]byte(testArtifactSQLite+"\n"), "sqlite"); !ok || artifact != testArtifactSQLite {
		t.Fatalf("sqlite receipt = %q valid=%v", artifact, ok)
	}
	if artifact, ok := validateReceipt([]byte(testArtifactPG+"\n"), "postgres"); !ok || artifact != testArtifactPG {
		t.Fatalf("postgres receipt = %q valid=%v", artifact, ok)
	}
	for _, test := range []struct {
		receipt string
		engine  string
	}{
		{receipt: testArtifactSQLite + "\n", engine: "postgres"},
		{receipt: testArtifactPG + "\n", engine: "sqlite"},
		{receipt: testArtifactSQLite + "\nextra", engine: "sqlite"},
	} {
		if _, ok := validateReceipt([]byte(test.receipt), test.engine); ok {
			t.Fatalf("accepted invalid %q for %s", test.receipt, test.engine)
		}
	}
}

func TestInitializeIsIdempotentAndReadOnlyMissingState(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	before, err := Read(context.Background(), cfg, func() time.Time { return now })
	if err != nil || before.Job.Status != JobStatusNotInitialized {
		t.Fatalf("Read before Initialize = %+v, %v", before, err)
	}
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)
	first, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["version"] != float64(1) || decoded["revision"] != float64(1) {
		t.Fatalf("initial state = %s", first)
	}
	if err := Initialize(context.Background(), cfg, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("idempotent Initialize rewrote state: %q != %q", first, second)
	}
	observation, err := Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
	if err != nil || observation.Job.Status != JobStatusNeverRun || observation.Job.Evidence != "job_record" {
		t.Fatalf("empty observation = %+v, %v", observation, err)
	}
	if observation.SourceID() == "" || observation.Revision() != 1 || !observation.StateValid() {
		t.Fatalf("observation metadata = source=%q revision=%d valid=%v", observation.SourceID(), observation.Revision(), observation.StateValid())
	}

}
func TestOneShotStateInspectionErrorPreservesEvidenceAndReportsUnavailable(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	originalLstat := stateFileLstat
	defer func() { stateFileLstat = originalLstat }()
	var calls, faultAt int
	stateFileLstat = func(root *os.Root, name string) (os.FileInfo, error) {
		calls++
		if calls == faultAt {
			return nil, syscall.EIO
		}
		return root.Lstat(name)
	}
	faultAt = 1
	if err := Initialize(context.Background(), cfg, now.Add(time.Hour)); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("Initialize inspection failure = %v, want unavailable", err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("Initialize overwrote state after inspection failure: before=%q after=%q", before, after)
	}

	calls = 0
	observation, err := Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
	if err != nil || observation.Job.Status != JobStatusStateUnavailable || observation.StatePresent() {
		t.Fatalf("initial Read inspection failure = %+v, %v; want unavailable with unknown presence", observation, err)
	}
	faultAt = 2
	calls = 0
	observation, err = Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
	if err != nil || observation.Job.Status != JobStatusStateUnavailable || !observation.StatePresent() {
		t.Fatalf("post-stat Read inspection failure = %+v, %v; want unavailable with present state", observation, err)
	}
}

func TestStrictStateProtocolRejectsUnknownDuplicateAndOversized(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)
	for _, content := range []string{
		`{"version":1,"version":1,"source_id":"00000000000000000000000000000000","revision":1,"engine":"sqlite","initialized_at":"2026-10-09T01:00:00Z"}`,
		`{"version":1,"source_id":"00000000000000000000000000000000","revision":1,"engine":"sqlite","initialized_at":"2026-10-09T01:00:00Z","unexpected":true}`,
		`{"version":1,"source_id":"00000000000000000000000000000000","revision":1,"engine":"sqlite","initialized_at":"2026-10-09T01:00:00Z"} {}`,
	} {
		if err := os.WriteFile(statePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		observation, err := Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
		if err != nil || observation.Job.Status != JobStatusStateInvalid {
			t.Fatalf("invalid protocol %q -> %+v, %v", content, observation, err)
		}

		if err := Initialize(context.Background(), cfg, now.Add(time.Hour)); !errors.Is(err, ErrStateInvalid) {
			t.Fatalf("Initialize accepted invalid protocol %q: %v", content, err)
		}
		preserved, readErr := os.ReadFile(statePath)
		if readErr != nil || string(preserved) != content {
			t.Fatalf("Initialize rewrote invalid protocol: %q, %v", preserved, readErr)
		}
	}
	if err := os.WriteFile(statePath, make([]byte, maxStateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
	if err != nil || observation.Job.Status != JobStatusStateInvalid {
		t.Fatalf("oversized state = %+v, %v", observation, err)
	}
}

func TestAtomicPublishRenameFailureIsBounded(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	oldRename := stateRenameFile
	stateRenameFile = func(root *os.Root, oldName, newName string) error {
		return errors.New("injected rename failure")
	}
	err := Initialize(context.Background(), cfg, now)
	stateRenameFile = oldRename
	if !errors.Is(err, ErrStatePublishFailed) {
		t.Fatalf("injected rename error = %v", err)
	}
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatalf("Initialize after injected rename = %v", err)
	}
}

func TestReversedTimestampIsClockAnomalyNotInvalid(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	reversed := `{"version":1,"source_id":"00000000000000000000000000000000","revision":2,"engine":"sqlite","initialized_at":"2026-10-09T01:00:00Z","latest_attempt":{"run_id":"22222222222222222222222222222222","started_at":"2026-10-09T01:02:00Z","result":"failed","finished_at":"2026-10-09T01:01:00Z","failure_code":"backup_failed"},"last_success":{"run_id":"11111111111111111111111111111111","started_at":"2026-10-09T01:00:00Z","finished_at":"2026-10-09T01:00:30Z","artifact_name":"xirang-sqlite-20261009-020000.db"}}`
	statePath := filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)
	if err := os.WriteFile(statePath, []byte(reversed), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(3 * time.Minute) })
	if err != nil || observation.Job.Status != JobStatusClockAnomaly {
		t.Fatalf("reversed timestamps = %+v, %v", observation, err)
	}

	setTestState(t, cfg, func(state *stateDocument) {
		state.Revision = 3
		state.InitializedAt = initialized.Add(time.Hour)
		state.LatestAttempt = nil
		state.LastSuccess = nil
	})
	future, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(3 * time.Minute) })
	if err != nil || future.Job.Status != JobStatusClockAnomaly {
		t.Fatalf("future initialized_at = %+v, %v", future, err)
	}
}

func TestSecureRootEngineLockAndUTF8Rejection(t *testing.T) {
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)

	rootConfig := testConfig(t)
	if err := Initialize(context.Background(), rootConfig, now); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(rootConfig.StateDirectory, rootLink); err != nil {
		t.Fatal(err)
	}
	rootLinked := rootConfig
	rootLinked.StateDirectory = rootLink
	if observation, err := Read(context.Background(), rootLinked, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateUnavailable {
		t.Fatalf("configured root symlink = %+v, %v", observation, err)
	}

	engineConfig := testConfig(t)
	if err := Initialize(context.Background(), engineConfig, now); err != nil {
		t.Fatal(err)
	}
	enginePath := filepath.Join(engineConfig.StateDirectory, engineConfig.Engine)
	engineTarget := enginePath + "-target"
	if err := os.Rename(enginePath, engineTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(engineTarget, enginePath); err != nil {
		t.Fatal(err)
	}
	if observation, err := Read(context.Background(), engineConfig, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateUnavailable {
		t.Fatalf("engine symlink = %+v, %v", observation, err)
	}

	lockConfig := testConfig(t)
	if err := Initialize(context.Background(), lockConfig, now); err != nil {
		t.Fatal(err)
	}
	runLockPath := filepath.Join(lockConfig.StateDirectory, lockConfig.Engine, runLockName)
	if err := os.Remove(runLockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", runLockPath); err != nil {
		t.Fatal(err)
	}
	if observation, err := Read(context.Background(), lockConfig, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateUnavailable {
		t.Fatalf("symlink lock replacement = %+v, %v", observation, err)
	}
	if err := os.Remove(runLockPath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(runLockPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if observation, err := Read(context.Background(), lockConfig, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateUnavailable {
		t.Fatalf("FIFO lock replacement = %+v, %v", observation, err)
	}

	stateConfig := testConfig(t)
	if err := Initialize(context.Background(), stateConfig, now); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateConfig.StateDirectory, stateConfig.Engine, stateFileName)
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(statePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if observation, err := Read(context.Background(), stateConfig, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateInvalid {
		t.Fatalf("FIFO state replacement = %+v, %v", observation, err)
	}

	utf8Config := testConfig(t)
	if err := Initialize(context.Background(), utf8Config, now); err != nil {
		t.Fatal(err)
	}
	utf8Path := filepath.Join(utf8Config.StateDirectory, utf8Config.Engine, stateFileName)
	if err := os.WriteFile(utf8Path, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	if observation, err := Read(context.Background(), utf8Config, func() time.Time { return now.Add(time.Minute) }); err != nil || observation.Job.Status != JobStatusStateInvalid {
		t.Fatalf("invalid UTF-8 state = %+v, %v", observation, err)
	}
}

func TestReadLockExclusionAndLockedCallback(t *testing.T) {
	cfg := testConfig(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, now); err != nil {
		t.Fatal(err)
	}
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
	observation, err := Read(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) })
	if err != nil || observation.Job.Status != JobStatusStateUnavailable {
		t.Fatalf("busy state lock = %+v, %v", observation, err)
	}
	if err := lockFile.Close(); err != nil {
		t.Fatal(err)
	}
	storage.close()

	called := false
	callbackErr := WithLockedObservation(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) }, func(observation Observation) error {
		called = true
		probeStorage, openErr := openExistingStorage(cfg)
		if openErr != nil {
			return openErr
		}
		defer probeStorage.close()
		probe, openErr := openLock(probeStorage.engine, stateLockName)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = probe.Close() }()
		if acquireErr := acquireLock(context.Background(), probe, 0, ErrStateUnavailable); !errors.Is(acquireErr, ErrStateUnavailable) {
			return fmt.Errorf("callback did not retain state lock: %v", acquireErr)
		}
		if observation.Job.Status != JobStatusNeverRun {
			return fmt.Errorf("callback observation = %+v", observation)
		}
		return nil
	})
	if callbackErr != nil || !called {
		t.Fatalf("locked callback = %v called=%v", callbackErr, called)
	}
	if err := os.Remove(filepath.Join(cfg.StateDirectory, cfg.Engine, stateFileName)); err != nil {
		t.Fatal(err)
	}
	missingCallback := false
	if err := WithLockedObservation(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) }, func(observation Observation) error {
		missingCallback = true
		if observation.Job.Status != JobStatusNotInitialized || observation.StatePresent() {
			return fmt.Errorf("missing state callback = %+v", observation)
		}
		return nil
	}); err != nil || !missingCallback {
		t.Fatalf("missing state callback = %v called=%v", err, missingCallback)
	}
	if err := os.Remove(filepath.Join(cfg.StateDirectory, cfg.Engine, stateLockName)); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := WithLockedObservation(context.Background(), cfg, func() time.Time { return now.Add(time.Minute) }, func(Observation) error {
		called = true
		return nil
	}); !errors.Is(err, ErrStateUnavailable) || called {
		t.Fatalf("missing state lock = %v called=%v", err, called)
	}
}

func TestInterruptedDerivationClockAnomalyAndLastSuccessPreservation(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	setTestState(t, cfg, func(state *stateDocument) {
		state.Revision = 2
		state.LatestAttempt = &stateAttempt{RunID: "11111111111111111111111111111111", StartedAt: initialized.Add(time.Minute), Result: AttemptRunning}
	})
	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(2 * time.Minute) })
	if err != nil || observation.Job.Status != JobStatusInterrupted {
		t.Fatalf("stale running without lock = %+v, %v", observation, err)
	}
	setTestState(t, cfg, func(state *stateDocument) {
		lastStart := initialized
		lastFinish := initialized.Add(30 * time.Second)
		start := initialized.Add(time.Minute)
		finish := initialized.Add(2 * time.Minute)
		state.LatestAttempt = &stateAttempt{RunID: "22222222222222222222222222222222", StartedAt: start, Result: AttemptFailed, FinishedAt: &finish, FailureCode: FailureBackupFailed}
		state.LastSuccess = &stateSuccess{RunID: "11111111111111111111111111111111", StartedAt: lastStart, FinishedAt: lastFinish, ArtifactName: testArtifactSQLite}
	})
	observation, err = Read(context.Background(), cfg, func() time.Time { return initialized.Add(3 * time.Minute) })
	if err != nil || observation.Job.Status != JobStatusFailed || observation.Job.LastSuccess == nil || observation.Job.LastSuccess.RunID != "11111111111111111111111111111111" {
		t.Fatalf("failed with preserved last success = %+v, %v", observation, err)
	}
}

func TestRunReceiptExitValidationAndCancellation(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	oldClock := runnerNow
	defer restoreRunnerClock(oldClock)
	clockValues := []time.Time{initialized.Add(time.Second), initialized.Add(2 * time.Second)}
	clockIndex := 0
	runnerNow = func() time.Time {
		value := clockValues[clockIndex]
		if clockIndex < len(clockValues)-1 {
			clockIndex++
		}
		return value
	}
	successScript := helperScript(t, "success", "")
	if err := Run(context.Background(), cfg, successScript, t.TempDir()); err != nil {
		t.Fatalf("successful Run = %v", err)
	}
	observation, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || observation.Job.Status != JobStatusSuccess || observation.Job.LastSuccess == nil || observation.Job.LastSuccess.ArtifactName != testArtifactSQLite {
		t.Fatalf("successful state = %+v, %v", observation, err)
	}

	failedAfterSuccess := helperScript(t, "failed", "")
	if err := Run(context.Background(), cfg, failedAfterSuccess, t.TempDir()); !errors.Is(err, ErrBackupFailed) {
		t.Fatalf("failed follow-up Run = %v", err)
	}
	afterFailure, err := Read(context.Background(), cfg, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || afterFailure.Job.Status != JobStatusFailed || afterFailure.Job.LastSuccess == nil || afterFailure.Job.LastSuccess.ArtifactName != testArtifactSQLite {
		t.Fatalf("failed follow-up state = %+v, %v", afterFailure, err)
	}

	cfgInvalid := testConfig(t)
	if err := Initialize(context.Background(), cfgInvalid, initialized); err != nil {
		t.Fatal(err)
	}
	clockIndex = 0
	invalidScript := helperScript(t, "invalid", "")
	if err := Run(context.Background(), cfgInvalid, invalidScript, t.TempDir()); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("invalid receipt Run = %v", err)
	}
	invalidObservation, err := Read(context.Background(), cfgInvalid, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || invalidObservation.Job.Status != JobStatusFailed || invalidObservation.Job.LatestAttempt == nil || invalidObservation.Job.LatestAttempt.FailureCode != FailureResultInvalid {
		t.Fatalf("invalid receipt state = %+v, %v", invalidObservation, err)
	}
	failedConfig := testConfig(t)
	if err := Initialize(context.Background(), failedConfig, initialized); err != nil {
		t.Fatal(err)
	}
	clockIndex = 0
	failedScript := helperScript(t, "failed", "")
	if err := Run(context.Background(), failedConfig, failedScript, t.TempDir()); !errors.Is(err, ErrBackupFailed) {
		t.Fatalf("non-zero Run = %v", err)
	}
	failedObservation, err := Read(context.Background(), failedConfig, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || failedObservation.Job.Status != JobStatusFailed || failedObservation.Job.LatestAttempt == nil || failedObservation.Job.LatestAttempt.FailureCode != FailureBackupFailed {
		t.Fatalf("non-zero state = %+v, %v", failedObservation, err)
	}

	startConfig := testConfig(t)
	if err := Initialize(context.Background(), startConfig, initialized); err != nil {
		t.Fatal(err)
	}
	clockIndex = 0
	missingScript := filepath.Join(t.TempDir(), "does-not-exist")
	if err := Run(context.Background(), startConfig, missingScript, t.TempDir()); !errors.Is(err, ErrBackupStartFailed) {
		t.Fatalf("start failure Run = %v", err)
	}
	startObservation, err := Read(context.Background(), startConfig, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || startObservation.Job.Status != JobStatusFailed || startObservation.Job.LatestAttempt == nil || startObservation.Job.LatestAttempt.FailureCode != FailureBackupStartFailed {
		t.Fatalf("start failure state = %+v, %v", startObservation, err)
	}

	cfgCancel := testConfig(t)
	if err := Initialize(context.Background(), cfgCancel, initialized); err != nil {
		t.Fatal(err)
	}
	clockIndex = 0
	barrier := filepath.Join(t.TempDir(), "started")
	barrierScript := helperScript(t, "barrier", barrier)
	runContext, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Run(runContext, cfgCancel, barrierScript, t.TempDir()) }()
	waitForFile(t, barrier)
	cancel()
	if err := <-result; !errors.Is(err, ErrProcessInterrupted) {
		t.Fatalf("canceled Run = %v", err)
	}
	interrupted, err := Read(context.Background(), cfgCancel, func() time.Time { return initialized.Add(3 * time.Second) })
	if err != nil || interrupted.Job.Status != JobStatusInterrupted {
		t.Fatalf("canceled state = %+v, %v", interrupted, err)
	}
}

func TestRunInheritedLockExclusionWithBarrier(t *testing.T) {
	cfg := testConfig(t)
	initialized := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	if err := Initialize(context.Background(), cfg, initialized); err != nil {
		t.Fatal(err)
	}
	oldClock := runnerNow
	defer restoreRunnerClock(oldClock)
	runnerNow = func() time.Time { return initialized.Add(time.Second) }
	barrier := filepath.Join(t.TempDir(), "descendant-started")
	descendantScript := helperScript(t, "descendant", barrier)
	runContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Run(runContext, cfg, descendantScript, t.TempDir()) }()
	waitForFile(t, barrier)
	if err := Run(context.Background(), cfg, descendantScript, t.TempDir()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run while inherited lock = %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, ErrProcessInterrupted) {
		t.Fatalf("descendant cancellation = %v", err)
	}
}

func setTestState(t *testing.T, cfg Config, mutate func(*stateDocument)) {
	t.Helper()
	storage, err := openExistingStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.close()
	lockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := acquireLock(context.Background(), lockFile, time.Second, ErrStateUnavailable); err != nil {
		t.Fatal(err)
	}
	state, present, err := readStateFile(storage)
	if err != nil || !present {
		t.Fatalf("read test state: %v present=%v", err, present)
	}
	mutate(&state)
	if err := validateState(state, cfg); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(storage, state); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("barrier %s was not reached", path)
}
