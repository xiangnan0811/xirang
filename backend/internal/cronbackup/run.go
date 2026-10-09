package cronbackup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"xirang/backend/internal/logger"
)

const (
	groupTerminationGrace       = 5 * time.Second
	groupTerminationKillGrace   = time.Second
	receiptReadTimeout          = 100 * time.Millisecond
	receiptPollInterval         = 2 * time.Millisecond
	finalPublicationLockTimeout = 10 * time.Second
)

var runnerNow = func() time.Time { return time.Now().UTC() }
var receiptReadHook func()

// Run executes one real backup script under the durable run lock. The script
// is executed directly (never through a shell), receives receipt FD3 and the
// inherited run-lock FD4, and has its output discarded. A success requires a
// zero exit status and exactly one valid basename receipt.
func Run(ctx context.Context, cfg Config, scriptPath, outputDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if scriptPath == "" || outputDir == "" {
		return fmt.Errorf("%w: script and output directory are required", ErrInvalidConfiguration)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	startTime, err := operationTime(runnerNow())
	if err != nil {
		return err
	}
	storage, err := openExistingStorage(cfg)
	if errors.Is(err, ErrNotInitialized) {
		return err
	}
	if err != nil {
		return err
	}
	defer storage.close()

	runLockFile, err := openLock(storage.engine, runLockName)
	if err != nil {
		return err
	}
	defer func() {
		if runLockFile != nil {
			_ = runLockFile.Close()
		}
	}()
	if err := acquireLock(ctx, runLockFile, 0, ErrAlreadyRunning); err != nil {
		return err
	}

	initialState, runID, err := beginRun(ctx, storage, startTime)
	if err != nil {
		if errors.Is(err, ErrStatePublishFailed) {
			return ErrStatePublishFailed
		}
		return err
	}
	runLog := logger.Module("cron_backup")
	runLog.Info().Str("run_id", runID).Str("code", "backup_started").Msg("backup_started")

	outcome := executeBackup(ctx, scriptPath, outputDir, runLockFile, cfg.Engine)
	if outcome.reacquireRunLock {
		// The inherited descriptor must be closed before this independent
		// acquisition. Closing releases only this process's flock; descendants
		// that escaped the process group continue to hold their inherited FD4.
		_ = runLockFile.Close()
		runLockFile = nil
		reacquired, reacquireErr := reopenRunLock(storage.engine)
		if reacquireErr != nil {
			outcome.result = AttemptInterrupted
			outcome.failureCode = FailureProcessInterrupted
			outcome.leaveRunning = true
		} else {
			runLockFile = reacquired
		}
	}
	if outcome.leaveRunning {
		runLog.Warn().Str("run_id", runID).Str("code", FailureProcessInterrupted).Msg("backup_interrupted")
		return outcome.err()
	}
	if err := finishRun(storage, cfg, initialState.SourceID, runID, outcome); err != nil {
		runLog.Error().Str("run_id", runID).Str("code", "state_publish_failed").Msg("state_publish_failed")
		if errors.Is(err, ErrStatePublishFailed) {
			return ErrStatePublishFailed
		}
		return err
	}
	if outcome.result == AttemptSuccess {
		runLog.Info().Str("run_id", runID).Str("code", "backup_success").Msg("backup_success")
	} else {
		runLog.Warn().Str("run_id", runID).Str("code", outcome.failureCode).Msg("backup_failed")
	}
	return outcome.err()
}

func reopenRunLock(root *os.Root) (*os.File, error) {
	lockFile, err := openLock(root, runLockName)
	if err != nil {
		return nil, err
	}
	if err := acquireLock(context.Background(), lockFile, 0, ErrAlreadyRunning); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return lockFile, nil
}

type runOutcome struct {
	result           AttemptResult
	failureCode      string
	artifact         string
	canceled         bool
	leaveRunning     bool
	reacquireRunLock bool
	startErr         error
	waitErr          error
}

func (outcome runOutcome) err() error {
	if outcome.result == AttemptSuccess {
		return nil
	}
	if outcome.startErr != nil {
		return ErrBackupStartFailed
	}
	switch outcome.failureCode {
	case FailureProcessInterrupted:
		return ErrProcessInterrupted
	case FailureResultInvalid:
		return ErrResultInvalid
	default:
		return ErrBackupFailed
	}
}

func beginRun(ctx context.Context, storage *rootedStorage, now time.Time) (stateDocument, string, error) {
	lockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		return stateDocument{}, "", err
	}
	defer func() { _ = lockFile.Close() }()
	if err := acquireLock(ctx, lockFile, stateLockWorkerTimeout, ErrStateUnavailable); err != nil {
		return stateDocument{}, "", err
	}
	state, present, readErr := readStateFile(storage)
	if readErr != nil {
		if errors.Is(readErr, ErrStateInvalid) {
			return stateDocument{}, "", ErrStateInvalid
		}
		return stateDocument{}, "", readErr
	}
	if !present {
		return stateDocument{}, "", ErrNotInitialized
	}
	if hasClockAnomaly(state, now) {
		return stateDocument{}, "", ErrClockAnomaly
	}
	if now.Before(state.InitializedAt) {
		return stateDocument{}, "", ErrClockAnomaly
	}
	if state.LatestAttempt != nil && state.LatestAttempt.Result == AttemptRunning {
		if now.Before(state.LatestAttempt.StartedAt) {
			return stateDocument{}, "", ErrClockAnomaly
		}
		interrupted := cloneState(state)
		interrupted.Revision, err = nextRevision(interrupted.Revision)
		if err != nil {
			return stateDocument{}, "", fmt.Errorf("%w: %v", ErrStatePublishFailed, err)
		}
		detected := now
		interrupted.LatestAttempt.Result = AttemptInterrupted
		interrupted.LatestAttempt.FinishedAt = nil
		interrupted.LatestAttempt.DetectedAt = &detected
		interrupted.LatestAttempt.FailureCode = FailureProcessInterrupted
		interrupted.LatestAttempt.ArtifactName = ""
		if err := validateState(interrupted, storage.cfg); err != nil {
			return stateDocument{}, "", fmt.Errorf("%w: interrupted state: %v", ErrStatePublishFailed, err)
		}
		if err := writeStateFile(storage, interrupted); err != nil {
			return stateDocument{}, "", err
		}
		state = interrupted
	}
	runID, err := randomHexID()
	if err != nil {
		return stateDocument{}, "", fmt.Errorf("%w: run identity: %v", ErrStatePublishFailed, err)
	}
	revision, err := nextRevision(state.Revision)
	if err != nil {
		return stateDocument{}, "", fmt.Errorf("%w: %v", ErrStatePublishFailed, err)
	}
	state.Revision = revision
	state.LatestAttempt = &stateAttempt{RunID: runID, StartedAt: now, Result: AttemptRunning}
	if err := validateState(state, storage.cfg); err != nil {
		return stateDocument{}, "", fmt.Errorf("%w: running state: %v", ErrStatePublishFailed, err)
	}
	if err := writeStateFile(storage, state); err != nil {
		return stateDocument{}, "", err
	}
	return state, runID, nil
}

func finishRun(storage *rootedStorage, cfg Config, sourceID, runID string, outcome runOutcome) error {
	lockFile, err := openLock(storage.engine, stateLockName)
	if err != nil {
		return fmt.Errorf("%w: state lock: %v", ErrStatePublishFailed, err)
	}
	defer func() { _ = lockFile.Close() }()
	background := context.Background()
	if err := acquireLock(background, lockFile, finalPublicationLockTimeout, ErrStatePublishFailed); err != nil {
		return fmt.Errorf("%w: %v", ErrStatePublishFailed, err)
	}
	state, present, readErr := readStateFile(storage)
	if !present || readErr != nil || state.SourceID != sourceID || state.LatestAttempt == nil || state.LatestAttempt.RunID != runID || state.LatestAttempt.Result != AttemptRunning {
		return fmt.Errorf("%w: current attempt changed", ErrStatePublishFailed)
	}
	finishTime, timeErr := operationTime(runnerNow())
	if timeErr != nil {
		return fmt.Errorf("%w: finish time: %v", ErrStatePublishFailed, timeErr)
	}
	if finishTime.Before(state.LatestAttempt.StartedAt) {
		return fmt.Errorf("%w: finish time before start", ErrStatePublishFailed)
	}
	state.Revision, err = nextRevision(state.Revision)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStatePublishFailed, err)
	}
	attempt := state.LatestAttempt
	switch outcome.result {
	case AttemptSuccess:
		attempt.Result = AttemptSuccess
		attempt.FinishedAt = &finishTime
		attempt.DetectedAt = nil
		attempt.FailureCode = ""
		attempt.ArtifactName = outcome.artifact
		state.LastSuccess = &stateSuccess{RunID: attempt.RunID, StartedAt: attempt.StartedAt, FinishedAt: finishTime, ArtifactName: outcome.artifact}
	case AttemptInterrupted:
		attempt.Result = AttemptInterrupted
		attempt.FinishedAt = nil
		attempt.DetectedAt = &finishTime
		attempt.FailureCode = FailureProcessInterrupted
		attempt.ArtifactName = ""
	case AttemptFailed:
		attempt.Result = AttemptFailed
		attempt.FinishedAt = &finishTime
		attempt.DetectedAt = nil
		attempt.FailureCode = outcome.failureCode
		attempt.ArtifactName = ""
	default:
		return fmt.Errorf("%w: unsupported outcome", ErrStatePublishFailed)
	}
	if err := validateState(state, cfg); err != nil {
		return fmt.Errorf("%w: final state: %v", ErrStatePublishFailed, err)
	}
	return writeStateFile(storage, state)
}

func executeBackup(ctx context.Context, scriptPath, outputDir string, runLockFile *os.File, engine string) runOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true}
	}
	signalContext, stopSignals := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	if err := signalContext.Err(); err != nil {
		return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true}
	}

	receiptRead, receiptWrite, err := os.Pipe()
	if err != nil {
		return runOutcome{result: AttemptFailed, failureCode: FailureBackupStartFailed, startErr: err}
	}
	defer func() { _ = receiptRead.Close() }()
	command := exec.CommandContext(context.Background(), scriptPath, outputDir)
	// nil directs os/exec to attach the child to /dev/null. Using an arbitrary
	// io.Writer would create copy pipes whose EOF waits for every descendant
	// that inherited stdout or stderr.
	command.Stdout = nil
	command.Stderr = nil
	command.ExtraFiles = []*os.File{receiptWrite, runLockFile}
	command.Env = receiptEnvironment(os.Environ())
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := signalContext.Err(); err != nil {
		_ = receiptWrite.Close()
		return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true}
	}
	if err := command.Start(); err != nil {
		_ = receiptWrite.Close()
		return runOutcome{result: AttemptFailed, failureCode: FailureBackupStartFailed, startErr: err}
	}
	_ = receiptWrite.Close()

	waitDone := make(chan error, 1)
	go waitWithoutReap(command.Process.Pid, waitDone)
	var (
		waitStatusErr error
		waitObserved  bool
		canceled      bool
		groupGone     bool
	)
	select {
	case waitStatusErr = <-waitDone:
		waitObserved = true
		if signalContext.Err() != nil {
			canceled = true
			groupGone = terminateProcessGroup(command.Process.Pid)
		}
	case <-signalContext.Done():
		canceled = true
		groupGone = terminateProcessGroup(command.Process.Pid)
		waitStatusErr, waitObserved = receiveWaitResult(waitDone, receiptReadTimeout)
	}

	if !waitObserved {
		if canceled {
			return finishCanceledBackup(command, waitDone, waitObserved, waitStatusErr, groupGone, "", false)
		}
		backgroundReap(command, waitDone, false)
		return runOutcome{result: AttemptFailed, failureCode: FailureBackupFailed, leaveRunning: true}
	}
	if waitStatusErr != nil {
		if canceled {
			return finishCanceledBackup(command, waitDone, waitObserved, waitStatusErr, groupGone, "", false)
		}
		backgroundReap(command, nil, true)
		return runOutcome{result: AttemptFailed, failureCode: FailureBackupFailed, leaveRunning: true, waitErr: waitStatusErr}
	}

	// Keep the leader pinned as a zombie while receipt validation and the final
	// cancellation arbitration run. This is the last point where a newly seen
	// cancellation may clean up same-group descendants before completion.
	receipt := readBoundedReceipt(receiptRead)
	artifact, valid := validateReceipt(receipt, engine)
	if !canceled && (ctx.Err() != nil || signalContext.Err() != nil) {
		canceled = true
		groupGone = terminateProcessGroup(command.Process.Pid)
	}
	if canceled {
		return finishCanceledBackup(command, waitDone, waitObserved, waitStatusErr, groupGone, artifact, valid)
	}

	// No cancellation is consulted after this reap. The completion decision
	// linearized while the leader was still pinned above.
	waitErr := command.Wait()
	if waitErr != nil {
		return runOutcome{result: AttemptFailed, failureCode: FailureBackupFailed, waitErr: waitErr}
	}
	if !valid {
		return runOutcome{result: AttemptFailed, failureCode: FailureResultInvalid}
	}
	return runOutcome{result: AttemptSuccess, artifact: artifact}
}

func finishCanceledBackup(command *exec.Cmd, waitDone <-chan error, waitObserved bool, waitStatusErr error, groupGone bool, artifact string, valid bool) runOutcome {
	if !waitObserved {
		backgroundReap(command, waitDone, false)
		return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true, leaveRunning: true}
	}
	if waitStatusErr != nil || !groupGone {
		backgroundReap(command, nil, true)
		return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true, leaveRunning: true, waitErr: waitStatusErr}
	}
	waitErr := command.Wait()
	if waitErr == nil && valid {
		return runOutcome{result: AttemptSuccess, artifact: artifact, canceled: true, reacquireRunLock: true}
	}
	return runOutcome{result: AttemptInterrupted, failureCode: FailureProcessInterrupted, canceled: true, reacquireRunLock: true, waitErr: waitErr}
}

func receiveWaitResult(done <-chan error, timeout time.Duration) (error, bool) {
	if timeout <= 0 {
		select {
		case err := <-done:
			return err, true
		default:
			return nil, false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

func backgroundReap(command *exec.Cmd, waitDone <-chan error, waitObserved bool) {
	if waitObserved {
		go func() { _ = command.Wait() }()
		return
	}
	go func() {
		<-waitDone
		_ = command.Wait()
	}()
}

func waitWithoutReap(pid int, result chan<- error) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		result <- err
		return
	}
}

type groupLiveness uint8

const (
	groupLivenessUnknown groupLiveness = iota
	groupLivenessLive
	groupLivenessGone
)

func terminateProcessGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	if waitForGroupGone(pid, time.Now().Add(groupTerminationGrace)) {
		return true
	}
	_ = signalProcessGroup(pid, syscall.SIGKILL)
	return waitForGroupGone(pid, time.Now().Add(groupTerminationKillGrace))
}

func waitForGroupGone(pid int, deadline time.Time) bool {
	for {
		if processGroupGone(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(receiptPollInterval)
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupGone(pid int) bool {
	return processGroupLiveness(pid) == groupLivenessGone
}

type procStat struct {
	state byte
	pgrp  int
}

func processGroupLiveness(pgid int) groupLiveness {
	if pgid <= 0 {
		return groupLivenessUnknown
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return groupLivenessUnknown
	}
	unknown := false
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := readProcStat("/proc/" + entry.Name() + "/stat")
		if err != nil {
			disappeared, matches := procEntryStatus(pid, pgid)
			if disappeared || !matches {
				continue
			}
			unknown = true
			continue
		}
		if stat.pgrp != pgid {
			continue
		}
		threadState := processThreadLiveness(pid, pgid)
		if procStateLive(stat.state) || threadState == groupLivenessLive {
			return groupLivenessLive
		}
		if threadState == groupLivenessUnknown {
			unknown = true
		}
	}
	if unknown {
		return groupLivenessUnknown
	}
	return groupLivenessGone
}

func procEntryStatus(pid, pgid int) (disappeared, matches bool) {
	if pid <= 0 {
		return false, true
	}
	actual, err := syscall.Getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrNotExist) {
			return true, false
		}
		if errors.Is(err, syscall.EPERM) {
			// Permission failures do not establish whether this process is
			// in the target group; retain the conservative unknown result.
			return false, true
		}
		return false, true
	}
	return false, actual == pgid
}

func processThreadLiveness(pid, pgid int) groupLiveness {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return groupLivenessGone
		}
		return groupLivenessUnknown
	}
	unknown := false
	for _, entry := range entries {
		tid, err := strconv.Atoi(entry.Name())
		if err != nil || tid <= 0 {
			continue
		}
		stat, err := readProcStat(fmt.Sprintf("/proc/%d/task/%d/stat", pid, tid))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			unknown = true
			continue
		}
		if stat.pgrp == pgid && procStateLive(stat.state) {
			return groupLivenessLive
		}
	}
	if unknown {
		return groupLivenessUnknown
	}
	return groupLivenessGone
}

func readProcStat(path string) (procStat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return procStat{}, err
	}
	open := bytes.IndexByte(data, '(')
	close := bytes.LastIndex(data, []byte(") "))
	if open <= 0 || close <= open || close+2 >= len(data) {
		return procStat{}, errors.New("malformed proc stat")
	}
	if _, err := strconv.Atoi(string(bytes.TrimSpace(data[:open]))); err != nil {
		return procStat{}, errors.New("malformed proc pid")
	}
	fields := bytes.Fields(data[close+2:])
	if len(fields) < 3 || len(fields[0]) != 1 {
		return procStat{}, errors.New("malformed proc fields")
	}
	pgrp, err := strconv.Atoi(string(fields[2]))
	if err != nil || pgrp <= 0 {
		return procStat{}, errors.New("malformed proc pgrp")
	}
	return procStat{state: fields[0][0], pgrp: pgrp}, nil
}

func procStateLive(state byte) bool {
	return state != 'Z' && state != 'X' && state != 'x'
}

func readBoundedReceipt(file *os.File) []byte {
	if file == nil {
		return nil
	}
	fd := int(file.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil
	}
	deadline := time.Now().Add(receiptReadTimeout)
	var receipt bytes.Buffer
	buffer := make([]byte, 128)
	receiptHookCalled := false
	for time.Now().Before(deadline) {
		// Use the raw syscall so O_NONBLOCK yields EAGAIN instead of the
		// os.File poller waiting for EOF from an inherited descriptor.
		read, err := unix.Read(fd, buffer)
		if read == 0 && err == nil {
			return receipt.Bytes()
		}
		if read > 0 {
			if receipt.Len()+read > maxReceiptBytes {
				return receipt.Bytes()
			}
			_, _ = receipt.Write(buffer[:read])
			if !receiptHookCalled {
				receiptHookCalled = true
				if hook := receiptReadHook; hook != nil {
					hook()
				}
			}
			if bytes.Contains(receipt.Bytes(), []byte{'\n'}) {
				// Once one newline is available, drain only immediately
				// available bytes. Never wait for EOF from descendants that
				// inherited FD3.
				for {
					extra, extraErr := unix.Read(fd, buffer)
					if extra == 0 && extraErr == nil {
						return receipt.Bytes()
					}
					if extra > 0 {
						if receipt.Len()+extra > maxReceiptBytes {
							return receipt.Bytes()
						}
						_, _ = receipt.Write(buffer[:extra])
						continue
					}
					if extraErr != nil && !errors.Is(extraErr, unix.EAGAIN) && !errors.Is(extraErr, unix.EWOULDBLOCK) {
						return receipt.Bytes()
					}
					return receipt.Bytes()
				}
			}
			continue
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return receipt.Bytes()
		}
		time.Sleep(receiptPollInterval)
	}
	return receipt.Bytes()
}

func validateReceipt(receipt []byte, engine string) (string, bool) {
	if len(receipt) == 0 || len(receipt) > maxReceiptBytes || bytes.Count(receipt, []byte{'\n'}) != 1 || receipt[len(receipt)-1] != '\n' {
		return "", false
	}
	name := string(receipt[:len(receipt)-1])
	if !validArtifactName(name, engine) {
		return "", false
	}
	return name, true
}

func receiptEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if len(entry) >= len("XIRANG_BACKUP_RECEIPT_FD=") && entry[:len("XIRANG_BACKUP_RECEIPT_FD=")] == "XIRANG_BACKUP_RECEIPT_FD=" {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "XIRANG_BACKUP_RECEIPT_FD=3")
}

func nextRevision(revision int64) (int64, error) {
	if revision == int64(^uint64(0)>>1) {
		return 0, fmt.Errorf("revision overflow")
	}
	if revision < 1 {
		return 0, fmt.Errorf("invalid revision")
	}
	return revision + 1, nil
}
