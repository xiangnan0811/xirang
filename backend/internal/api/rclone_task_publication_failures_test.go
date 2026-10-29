package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"

	"gorm.io/gorm"
)

var (
	errRcloneTaskReadOnlyFailure       = errors.New("controlled read-only source failure")
	errRcloneTaskCommitReadbackFailure = errors.New("controlled commit readback loss")
)

type rcloneTaskExecutionObservation struct {
	result executor.PublicationExecutionResult
	err    error
}

type rcloneTaskExecutionRecorder struct {
	mu    sync.Mutex
	byRun map[uint]rcloneTaskExecutionObservation
}

func (recorder *rcloneTaskExecutionRecorder) get(runID uint) (rcloneTaskExecutionObservation, bool) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	result, ok := recorder.byRun[runID]
	return result, ok
}

type rcloneTaskRecordingFactory struct {
	delegate executor.Factory
	recorder *rcloneTaskExecutionRecorder
}

func (factory *rcloneTaskRecordingFactory) Resolve(kind string) executor.Executor {
	resolved := factory.delegate.Resolve(kind)
	publicationExecutor, ok := resolved.(executor.PublicationExecutor)
	if kind != "rclone" || !ok {
		return resolved
	}
	return &rcloneTaskRecordingExecutor{Executor: resolved, publication: publicationExecutor, recorder: factory.recorder}
}

type rcloneTaskRecordingExecutor struct {
	executor.Executor
	publication executor.PublicationExecutor
	recorder    *rcloneTaskExecutionRecorder
}

func (recording *rcloneTaskRecordingExecutor) RunWithPublication(ctx context.Context, request executor.PublicationExecutionRequest, log executor.LogFunc, progress executor.ProgressFunc) (executor.PublicationExecutionResult, error) {
	result, err := recording.publication.RunWithPublication(ctx, request, log, progress)
	recording.recorder.mu.Lock()
	recording.recorder.byRun[request.TaskRunID] = rcloneTaskExecutionObservation{result: result, err: err}
	recording.recorder.mu.Unlock()
	return result, err
}

// The real SSH transport performs every command. Faults are injected only at
// selected external boundaries, after the fixture has activated a real binding.
type rcloneTaskFaultTransport struct {
	*rcloneHTTPAcceptanceTransport
	mu                    sync.Mutex
	mode, source, managed string
	fired                 bool
}

func (fault *rcloneTaskFaultTransport) arm(mode, source, managed string) {
	fault.mu.Lock()
	defer fault.mu.Unlock()
	fault.mode, fault.source, fault.managed, fault.fired = mode, source, managed, false
	fault.reset(managed)
}

func (fault *rcloneTaskFaultTransport) take(mode string) bool {
	fault.mu.Lock()
	defer fault.mu.Unlock()
	if fault.mode != mode || fault.fired {
		return false
	}
	fault.fired = true
	return true
}

func (fault *rcloneTaskFaultTransport) Run(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits) (provider.CommandOutput, error) {
	if invocation.Operation == provider.OperationRcloneManagedCopy && fault.take("positive-exit") {
		markers, err := filepath.Glob(filepath.Join(fault.managed, "points", "*", "control", "attempt.json"))
		if err != nil || len(markers) != 1 {
			return provider.CommandOutput{}, errors.New("real attempt marker was not present")
		}
		hidden := fault.source + ".temporarily-unavailable"
		if err := os.Rename(fault.source, hidden); err != nil {
			return provider.CommandOutput{}, err
		}
		output, runErr := fault.rcloneHTTPAcceptanceTransport.Run(ctx, invocation, limits)
		if restoreErr := os.Rename(hidden, fault.source); restoreErr != nil {
			return provider.CommandOutput{}, errors.Join(runErr, restoreErr)
		}
		return output, runErr
	}
	return fault.rcloneHTTPAcceptanceTransport.Run(ctx, invocation, limits)
}

func (fault *rcloneTaskFaultTransport) Open(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, maxBytes int64) (provider.ReadHandle, error) {
	if invocation.Operation == provider.OperationRcloneManagedRecursiveList && fault.take("before-mutation") {
		return nil, errRcloneTaskReadOnlyFailure
	}
	if invocation.Operation == provider.OperationRcloneManagedCat {
		commits, err := filepath.Glob(filepath.Join(fault.managed, "points", "*", "control", "commit.json"))
		if err == nil && len(commits) == 1 && fault.take("commit-readback") {
			handle, openErr := fault.rcloneHTTPAcceptanceTransport.Open(ctx, invocation, limits, maxBytes)
			if openErr != nil {
				return nil, openErr
			}
			return &rcloneTaskReadbackFailure{ReadHandle: handle}, nil
		}
	}
	return fault.rcloneHTTPAcceptanceTransport.Open(ctx, invocation, limits, maxBytes)
}

type rcloneTaskReadbackFailure struct{ provider.ReadHandle }

func (handle *rcloneTaskReadbackFailure) Close() error {
	return errors.Join(handle.ReadHandle.Close(), errRcloneTaskCommitReadbackFailure)
}

func rcloneTaskFaultDependencies(t *testing.T, binary string, transport **rcloneTaskFaultTransport, shortLease bool) rcloneHTTPDependencies {
	return func(db *gorm.DB, _ *rcloneHTTPClock) (provider.CommandTransport, provider.CommandStreamTransport, provider.StagedPayloadTransport, repository.RcloneNativePreflightFactoryBuilder, repository.RcloneNativeFactoryBuilder) {
		settingsService := settings.NewService(db)
		if shortLease {
			if err := settingsService.Update("backup_assets.lease_heartbeat", "10s"); err != nil {
				t.Fatal(err)
			}
			if err := settingsService.Update("backup_assets.lease_duration", "30s"); err != nil {
				t.Fatal(err)
			}
		}
		foundation := backupasset.NewFoundationService(settingsService)
		delegate, err := provider.NewSSHCommandTransportWithConcurrencySource(sshutil.NewNodeDialer(db), func() (int, error) {
			config, err := foundation.ProviderConfig()
			return config.MaxConcurrency, err
		}, provider.ToolBinaries{Rclone: binary})
		if err != nil {
			t.Fatal(err)
		}
		*transport = &rcloneTaskFaultTransport{rcloneHTTPAcceptanceTransport: &rcloneHTTPAcceptanceTransport{delegate: delegate}}
		return *transport, *transport, nil, nil, nil
	}
}

func TestRcloneTaskPublicationRealSSHFailures(t *testing.T) {
	binary := requireRealRclone(t)
	for _, mode := range []string{"before-mutation", "positive-exit", "commit-readback", "cancel", "transport-drop"} {
		t.Run(mode, func(t *testing.T) { runRcloneTaskPublicationRealSSHFailure(t, binary, mode) })
	}
}

func runRcloneTaskPublicationRealSSHFailure(t *testing.T, binary, mode string) {
	t.Helper()
	node := startRcloneHTTPSSH(t)
	var proxy *rcloneTaskSSHProxy
	if mode == "transport-drop" {
		proxy = newRcloneTaskSSHProxy(t, net.JoinHostPort(node.Host, strconv.Itoa(node.Port)))
		node.Host, node.Port = "127.0.0.1", proxy.port()
	}
	pidFile := filepath.Join(t.TempDir(), "owned-rclone-copy.pid")
	stoppedFile := pidFile + ".stopped"
	pid := 0
	commandBinary := binary
	if mode == "cancel" || mode == "transport-drop" {
		commandBinary = rcloneTaskSlowBinary(t, binary, pidFile, stoppedFile)
	}
	var fault *rcloneTaskFaultTransport
	var manager *task.Manager
	recorder := &rcloneTaskExecutionRecorder{byRun: make(map[uint]rcloneTaskExecutionObservation)}
	managerStopped := false
	cron := scheduler.NewCronScheduler()
	cron.Start()
	fixture := newRcloneHTTPFixtureWithRuntimeSetup(t, commandBinary, rcloneTaskFaultDependencies(t, commandBinary, &fault, mode == "commit-readback" || mode == "transport-drop"), func(runtime *backupruntime.Runtime, db *gorm.DB, settingsService *settings.Service) {
		factory := executor.NewFactoryWithPublicationStrategies("rsync", runtime.ResticPublicationStrategy(), runtime.RsyncTreePublicationStrategy(), runtime.RclonePublicationStrategy())
		manager = task.NewManager(db, &rcloneTaskRecordingFactory{delegate: factory, recorder: recorder}, nil, cron, settingsService, nil, 8, 90)
		manager.SetPublicationCoordinator(runtime.PublicationCoordinator())
		manager.SetLineageGuard(runtime.LineageGuard())
		manager.SetNodeWriteAdmission(runtime.NodeWriteCoordinator())
		manager.SetBackupSourceCompletionObserver(runtime.RepositoryService())
		manager.SetLegacyBlockRecorder(runtime.LegacyBlockRecorder())
		if err := runtime.SetCommitObserver(manager); err != nil {
			t.Fatal(err)
		}
		if err := runtime.SetInterruptedRunReporter(manager); err != nil {
			t.Fatal(err)
		}
		if err := runtime.SetInterruptedRunReadiness(manager); err != nil {
			t.Fatal(err)
		}
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if !managerStopped {
			if err := manager.Shutdown(ctx); err != nil {
				t.Errorf("stop real task manager: %v", err)
			}
		}
		cron.Stop()
	}()
	if err := fixture.setting.Update("backup_assets.rclone_portable_deadline", "5m"); err != nil {
		t.Fatal(err)
	}
	source, legacy, managed := makeRcloneHTTPPaths(t)
	id := fixture.addTask(node, "rclone-task-failure-"+mode, source, legacy)
	revision := fixture.portableCandidate(id, rcloneSFTPConfig(node), "archive:"+managed)
	ready := fixture.preflight(id, revision, backupasset.PublicationVersionedPrefix)
	fixture.activate(id, revision, ready.PreflightID, 200)
	if err := manager.Resume(id); err != nil {
		t.Fatal(err)
	}
	if mode == "cancel" || mode == "transport-drop" {
		if err := os.WriteFile(filepath.Join(source, "slow.bin"), make([]byte, 1<<20), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(pidFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ownedPID := pid
			if ownedPID == 0 {
				if data, err := os.ReadFile(pidFile); err == nil {
					ownedPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				}
			}
			if ownedPID > 0 {
				reapRcloneTaskPID(t, ownedPID, binary)
			}
		})
	}
	fault.arm(mode, source, managed)
	runID, err := manager.TriggerManual(id)
	if err != nil || runID == 0 {
		t.Fatalf("trigger real TaskRun: %v", err)
	}
	if mode == "cancel" || mode == "transport-drop" {
		resolvedBinary, err := filepath.EvalSymlinks(binary)
		if err != nil {
			t.Fatal(err)
		}
		waitRcloneTaskCondition(t, 90*time.Second, func() bool {
			paths, _ := filepath.Glob(filepath.Join(managed, "points", "*", "data"))
			data, readErr := os.ReadFile(pidFile)
			if len(paths) == 0 || readErr != nil {
				return false
			}
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid <= 0 {
				return false
			}
			executable, readErr := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			return readErr == nil && executable == resolvedBinary
		})
		if mode == "cancel" {
			if err := manager.Cancel(id); err != nil {
				t.Fatalf("cancel real publication: %v", err)
			}
		} else {
			proxy.dropActive()
		}
	}
	var point model.RecoveryPoint
	var run model.TaskRun
	waitRcloneTaskCondition(t, 120*time.Second, func() bool {
		if err := fixture.db.First(&run, runID).Error; err != nil {
			return false
		}
		if err := fixture.db.Where("producing_task_run_id = ?", runID).First(&point).Error; err != nil {
			return false
		}
		consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		return err == nil && model.IsTerminalTaskRunStatus(run.Status) && (point.State == string(backupasset.RecoveryPointFailed) || consistency.Completion == backupasset.CompletionOutcomeUnknown)
	})
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		t.Fatal(err)
	}
	var effects int64
	if err := fixture.db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", runID, model.TaskRunEffectTypeRetry).Count(&effects).Error; err != nil {
		t.Fatal(err)
	}
	observation, observed := recorder.get(runID)
	if !observed {
		t.Fatal("production Rclone executor result was not observed")
	}
	assertRcloneRealFailureEvidence(t, fixture.db, mode, run, point, observation, effects)
	switch mode {
	case "before-mutation":
		if point.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailurePublicationPreconditionMissing || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced {
			t.Fatal("read-only preflight failure was not rejected with stopped writer evidence")
		}
		if markers, _ := filepath.Glob(filepath.Join(managed, "points", "*", "control", "attempt.json")); len(markers) != 0 {
			t.Fatal("read-only failure created an attempt marker")
		}
	case "positive-exit":
		if point.State != string(backupasset.RecoveryPointFailed) || consistency.Completion != backupasset.CompletionKnownNonzero || consistency.Code != backupasset.FailureProviderNonzeroExit || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced || effects != 1 {
			t.Fatal("real nonzero exit did not persist a retryable stopped-writer failure")
		}
		assertRcloneRealRetry(t, fixture, manager, id, runID, point, managed)
	case "commit-readback":
		if point.State != string(backupasset.RecoveryPointPreparing) || consistency.Completion != backupasset.CompletionOutcomeUnknown || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced || effects != 0 {
			t.Fatal("real commit plus injected read loss did not suppress retry")
		}
		if commits, _ := filepath.Glob(filepath.Join(managed, "points", "*", "control", "commit.json")); len(commits) != 1 {
			t.Fatal("readback loss occurred without a real commit")
		}
		assertRcloneRealCommittedReconciliation(t, fixture, fault, point, run, managed)
	case "cancel":
		stopped, stopErr := os.ReadFile(stoppedFile)
		_, processErr := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		childReaped := stopErr == nil && strings.TrimSpace(string(stopped)) == "stopped" && errors.Is(processErr, os.ErrNotExist)
		if run.Status != model.TaskRunStatusCanceled || point.State != string(backupasset.RecoveryPointFailed) || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced || effects != 0 || !childReaped {
			quiesced := "absent"
			if consistency.RcloneWriterQuiesced != nil {
				quiesced = strconv.FormatBool(*consistency.RcloneWriterQuiesced)
			}
			t.Fatalf("canceled writer outcome: task=%s point=%s completion=%s code=%s writer_quiesced=%s retry_effects=%d child_reaped=%t", run.Status, point.State, consistency.Completion, consistency.Code, quiesced, effects, childReaped)
		}
	case "transport-drop":
		if point.State != string(backupasset.RecoveryPointPreparing) || consistency.Completion != backupasset.CompletionOutcomeUnknown || consistency.RcloneWriterQuiesced != nil && *consistency.RcloneWriterQuiesced || effects != 0 {
			t.Fatal("lost SSH transport did not retain unknown writer protection")
		}
		reapRcloneTaskPID(t, pid, binary)
		assertRcloneRealUnknownReconciliation(t, fixture, fault, point, run, managed)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := manager.Shutdown(shutdownCtx); err != nil {
			cancel()
			t.Fatalf("stop task manager before restart: %v", err)
		}
		managerStopped = true
		cancel()
		assertRcloneUnknownRestart(t, fixture, commandBinary, managed, point.ID, run.ID)
	}
}

func assertRcloneRealFailureEvidence(t *testing.T, db *gorm.DB, mode string, run model.TaskRun, point model.RecoveryPoint, observation rcloneTaskExecutionObservation, effects int64) {
	t.Helper()
	if observation.err == nil || observation.result.ProviderCommit != nil || observation.result.EvidenceCode != "" || run.FinishedAt == nil {
		t.Fatal("failure lost its original error, durable completion, or result boundary")
	}
	var publicationErr *provider.RclonePublicationError
	if !errors.As(observation.err, &publicationErr) || publicationErr == nil {
		t.Fatal("production Rclone failure did not retain typed facts")
	}
	fact := provider.ClassifyRclonePublicationFailure(observation.err)
	stopped := fact.WriterQuiesced != nil && *fact.WriterQuiesced
	unknownResult := observation.result.ExitCode == provider.UnknownProviderExitCode && observation.result.Completion == backupasset.CompletionOutcomeUnknown
	switch mode {
	case "before-mutation":
		if !errors.Is(observation.err, errRcloneTaskReadOnlyFailure) || publicationErr.MutationAttempted || publicationErr.CommitAttempted || !fact.Unattempted || !stopped || observation.result.ExitCode != provider.UnknownProviderExitCode || observation.result.Completion != "" || run.Status != model.TaskRunStatusFailed || effects != 1 {
			t.Fatal("pre-mutation failure did not preserve its cause and original retry policy")
		}
	case "positive-exit":
		if !errors.Is(observation.err, sshutil.ErrCommandFailed) || !publicationErr.MutationAttempted || publicationErr.CommitAttempted || !fact.KnownNonzero || !stopped || fact.ExitCode <= 0 || observation.result.ExitCode != fact.ExitCode || observation.result.Completion != backupasset.CompletionKnownNonzero || run.Status != model.TaskRunStatusFailed || effects != 1 {
			t.Fatal("actual nonzero exit did not reach the task executor with retryable evidence")
		}
	case "cancel":
		if !errors.Is(observation.err, context.Canceled) || !publicationErr.MutationAttempted || publicationErr.CommitAttempted || !fact.CompletionUnproven || !stopped || observation.result.ExitCode != provider.UnknownProviderExitCode || observation.result.Completion != "" || run.Status != model.TaskRunStatusCanceled || effects != 0 {
			t.Fatal("cancellation lost its cause or confirmed writer exit")
		}
	case "commit-readback":
		if !errors.Is(observation.err, provider.ErrRcloneCommitOutcomeUnknown) || !errors.Is(observation.err, errRcloneTaskCommitReadbackFailure) || !publicationErr.MutationAttempted || !publicationErr.CommitAttempted || !stopped || fact.Unattempted || fact.KnownNonzero || fact.CompletionUnproven || !unknownResult || run.Status != model.TaskRunStatusFailed || effects != 0 {
			t.Fatal("commit readback loss did not preserve its cause and unknown outcome")
		}
	case "transport-drop":
		if !publicationErr.MutationAttempted || publicationErr.CommitAttempted || publicationErr.PositiveExitCode != 0 || fact.WriterQuiesced == nil || *fact.WriterQuiesced || fact.Unattempted || fact.KnownNonzero || fact.CompletionUnproven || !unknownResult || run.Status != model.TaskRunStatusFailed || effects != 0 {
			t.Fatal("transport loss incorrectly proved a writer exit or exit code")
		}
	default:
		t.Fatal("unsupported task failure mode")
	}
	var active, released int64
	query := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND holder_type = ?", point.ID, backupasset.LeaseHolderPointPublication)
	if err := query.Where("status = ?", backupasset.LeaseActive).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	query = db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND holder_type = ?", point.ID, backupasset.LeaseHolderPointPublication)
	if err := query.Where("status = ?", backupasset.LeaseReleased).Count(&released).Error; err != nil {
		t.Fatal(err)
	}
	if mode == "commit-readback" || mode == "transport-drop" {
		if active != 1 {
			t.Fatalf("unknown publication has %d active leases", active)
		}
	} else if active != 0 || released != 1 {
		t.Fatalf("terminal publication lease counts active=%d released=%d", active, released)
	}
}

func rcloneTaskAttempt(t *testing.T, point model.RecoveryPoint) (provider.RcloneAttemptV1, string) {
	t.Helper()
	var locator struct {
		TaggedAttempt string `json:"tagged_attempt"`
	}
	if err := json.Unmarshal([]byte(point.EncryptedProviderLocator), &locator); err != nil {
		t.Fatal("decode signed attempt locator")
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	lineage, lineageErr := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || lineageErr != nil || attempt.Portable == nil || !lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt) {
		t.Fatal("stored Rclone attempt and immutable deadline disagree")
	}
	return attempt, locator.TaggedAttempt
}

func rcloneTaskControl(t *testing.T, managed string, attempt provider.RcloneAttemptV1, name string) []byte {
	t.Helper()
	value, err := os.ReadFile(filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "control", name))
	if err != nil || len(value) == 0 {
		t.Fatal("expected real Rclone control is missing")
	}
	return value
}

func rcloneTaskPoint(t *testing.T, db *gorm.DB, pointID string) model.RecoveryPoint {
	t.Helper()
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatal(err)
	}
	return point
}

func rcloneTaskLeases(t *testing.T, db *gorm.DB, pointID string) []model.RecoveryPointLease {
	t.Helper()
	var leases []model.RecoveryPointLease
	if err := db.Where("recovery_point_id = ?", pointID).Order("id ASC").Find(&leases).Error; err != nil {
		t.Fatal(err)
	}
	return leases
}

func rcloneTaskAfterLeaseBeforeDeadline(t *testing.T, fixture *rcloneHTTPFixture, point model.RecoveryPoint, attempt provider.RcloneAttemptV1) {
	t.Helper()
	var lease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND holder_type = ? AND status = ?", point.ID, backupasset.LeaseHolderPointPublication, backupasset.LeaseActive).First(&lease).Error; err != nil {
		t.Fatal(err)
	}
	if !lease.AbsoluteDeadline.Equal(attempt.PointDeadlineAt) {
		t.Fatal("publication lease changed the original deadline")
	}
	now := lease.LeaseExpiresAt.Add(time.Second)
	if !now.Before(attempt.PointDeadlineAt) {
		t.Fatal("fixture lease does not expire before the publication deadline")
	}
	fixture.clock.Set(now)
}

func rcloneTaskRequireReconcileRead(t *testing.T, fault *rcloneTaskFaultTransport, previous int) {
	t.Helper()
	events := fault.snapshot()
	readControl := false
	for _, event := range events[previous:] {
		switch event.operation {
		case provider.OperationRcloneManagedCat:
			readControl = true
		case provider.OperationRcloneManagedCopy, provider.OperationRcloneManagedCopyTo, provider.OperationRcloneManagedNativeSync, provider.OperationRcloneManagedDeleteExactPrefix:
			t.Fatal("reconciliation attempted a remote publication mutation")
		}
	}
	if !readControl {
		t.Fatal("production reconciliation did not read a real remote control")
	}
}

func rcloneTaskRequireFailedRunUnchanged(t *testing.T, db *gorm.DB, run model.TaskRun) {
	t.Helper()
	var stored model.TaskRun
	if err := db.First(&stored, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != run.Status || stored.LastError != run.LastError || stored.FinishedAt == nil || run.FinishedAt == nil || !stored.FinishedAt.Equal(*run.FinishedAt) {
		t.Fatal("reconciliation changed the original failed TaskRun")
	}
	var effects int64
	if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeRetry).Count(&effects).Error; err != nil || effects != 0 {
		t.Fatalf("unknown publication gained a retry effect: %d %v", effects, err)
	}
	var runs int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ?", run.TaskID).Count(&runs).Error; err != nil || runs != 1 {
		t.Fatalf("unknown publication gained another TaskRun: %d %v", runs, err)
	}
}

func assertRcloneRealCommittedReconciliation(t *testing.T, fixture *rcloneHTTPFixture, fault *rcloneTaskFaultTransport, original model.RecoveryPoint, run model.TaskRun, managed string) {
	t.Helper()
	attempt, signedAttempt := rcloneTaskAttempt(t, original)
	attemptControl := rcloneTaskControl(t, managed, attempt, "attempt.json")
	commitControl := rcloneTaskControl(t, managed, attempt, "commit.json")
	rcloneTaskAfterLeaseBeforeDeadline(t, fixture, original, attempt)
	priorEvents := len(fault.snapshot())
	outcome, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointVerifying || !outcome.ProviderCommitRecorded {
		t.Fatalf("authenticated commit reconciliation failed: class=%s state=%s", rcloneHTTPSafeErrorClass(err), outcome.State)
	}
	rcloneTaskRequireReconcileRead(t, fault, priorEvents)
	verifying := rcloneTaskPoint(t, fixture.db, original.ID)
	_, verifyingAttempt := rcloneTaskAttempt(t, verifying)
	consistency, err := backupasset.DecodePublicationConsistency(verifying.ConsistencyJSON)
	if err != nil || verifyingAttempt != signedAttempt || verifying.LineageJSON != original.LineageJSON || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced {
		t.Fatal("reconciled authenticated commit lost the original attempt or stopped-writer fact")
	}
	outcome, err = fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointCommitted || !outcome.ProviderCommitRecorded {
		t.Fatalf("real committed manifest verification failed: class=%s state=%s", rcloneHTTPSafeErrorClass(err), outcome.State)
	}
	committed, _, committedAttempt, _ := fixture.portableHTTPPoint(original.ID)
	if !reflect.DeepEqual(committedAttempt, attempt) || committed.LineageJSON != original.LineageJSON ||
		!bytes.Equal(attemptControl, rcloneTaskControl(t, managed, attempt, "attempt.json")) || !bytes.Equal(commitControl, rcloneTaskControl(t, managed, attempt, "commit.json")) {
		t.Fatal("authenticated reconciliation changed immutable remote controls or deadline")
	}
	rcloneTaskRequireFailedRunUnchanged(t, fixture.db, run)
	fixture.clock.Set(attempt.PointDeadlineAt)
	priorEvents = len(fault.snapshot())
	if _, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("committed point was reprocessed at its deadline: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	after := rcloneTaskPoint(t, fixture.db, original.ID)
	if len(fault.snapshot()) != priorEvents || after.ConsistencyJSON != committed.ConsistencyJSON || after.EncryptedProviderLocator != committed.EncryptedProviderLocator || after.LineageJSON != committed.LineageJSON {
		t.Fatal("deadline reprocessed an already committed real point")
	}
}

func assertRcloneRealUnknownReconciliation(t *testing.T, fixture *rcloneHTTPFixture, fault *rcloneTaskFaultTransport, original model.RecoveryPoint, run model.TaskRun, managed string) {
	t.Helper()
	attempt, signedAttempt := rcloneTaskAttempt(t, original)
	attemptControl := rcloneTaskControl(t, managed, attempt, "attempt.json")
	dataRoot := filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "data")
	if info, err := os.Stat(dataRoot); err != nil || !info.IsDir() {
		t.Fatal("transport loss did not leave a real incomplete data root")
	}
	commitPath := filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "control", "commit.json")
	if _, err := os.Stat(commitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transport-loss attempt unexpectedly contains a commit")
	}
	rcloneTaskAfterLeaseBeforeDeadline(t, fixture, original, attempt)
	priorEvents := len(fault.snapshot())
	if _, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID); err == nil {
		t.Fatal("production reconciliation accepted an incomplete attempt without a commit")
	}
	rcloneTaskRequireReconcileRead(t, fault, priorEvents)
	beforeDeadline := rcloneTaskPoint(t, fixture.db, original.ID)
	_, beforeSigned := rcloneTaskAttempt(t, beforeDeadline)
	fact, err := backupasset.DecodePublicationConsistency(beforeDeadline.ConsistencyJSON)
	if err != nil || beforeDeadline.State != original.State || beforeDeadline.EncryptedProviderLocator != original.EncryptedProviderLocator ||
		beforeDeadline.LineageJSON != original.LineageJSON || beforeSigned != signedAttempt || fact.Completion != backupasset.CompletionOutcomeUnknown ||
		fact.RcloneWriterQuiesced != nil && *fact.RcloneWriterQuiesced {
		t.Fatal("incomplete real reconciliation lost the unknown writer protection")
	}
	if _, err := os.Stat(commitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete reconciliation created a remote commit")
	}
	if !bytes.Equal(attemptControl, rcloneTaskControl(t, managed, attempt, "attempt.json")) {
		t.Fatal("reconciliation changed the signed remote attempt")
	}
	rcloneTaskRequireFailedRunUnchanged(t, fixture.db, run)
	leases := rcloneTaskLeases(t, fixture.db, original.ID)
	fixture.clock.Set(attempt.PointDeadlineAt)
	priorEvents = len(fault.snapshot())
	outcome, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointPreparing || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatalf("unknown writer was not held exactly at deadline: class=%s state=%s", rcloneHTTPSafeErrorClass(err), outcome.State)
	}
	hold := rcloneTaskPoint(t, fixture.db, original.ID)
	holdFact, err := backupasset.DecodePublicationConsistency(hold.ConsistencyJSON)
	if err != nil || hold.State != original.State || hold.EncryptedProviderLocator != original.EncryptedProviderLocator || hold.LineageJSON != original.LineageJSON ||
		holdFact.Completion != backupasset.CompletionOutcomeUnknown || holdFact.Code != backupasset.FailurePublicationDeadlineExceeded ||
		holdFact.RcloneWriterQuiesced != nil && *holdFact.RcloneWriterQuiesced || holdFact.PublicationRevision != fact.PublicationRevision+1 ||
		!reflect.DeepEqual(leases, rcloneTaskLeases(t, fixture.db, original.ID)) || len(fault.snapshot()) != priorEvents {
		t.Fatal("deadline hold changed immutable evidence, leases, or contacted the provider")
	}
	for _, now := range []time.Time{attempt.PointDeadlineAt, attempt.PointDeadlineAt.Add(time.Second)} {
		fixture.clock.Set(now)
		repeated, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), original.ID)
		after := rcloneTaskPoint(t, fixture.db, original.ID)
		if err != nil || repeated.State != backupasset.RecoveryPointPreparing || repeated.Code != backupasset.FailurePublicationDeadlineExceeded ||
			after.ConsistencyJSON != hold.ConsistencyJSON || after.EncryptedProviderLocator != hold.EncryptedProviderLocator || after.LineageJSON != hold.LineageJSON ||
			after.PointRevision != hold.PointRevision || !after.UpdatedAt.Equal(hold.UpdatedAt) || !reflect.DeepEqual(leases, rcloneTaskLeases(t, fixture.db, original.ID)) || len(fault.snapshot()) != priorEvents {
			t.Fatal("repeated unknown deadline hold changed state, lease, or performed provider I/O")
		}
	}
	if !bytes.Equal(attemptControl, rcloneTaskControl(t, managed, attempt, "attempt.json")) {
		t.Fatal("deadline processing changed the signed remote attempt")
	}
	if _, err := os.Stat(commitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deadline processing created a remote commit")
	}
	var activeManifests int64
	if err := fixture.db.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ? AND is_active = ?", original.ID, true).Count(&activeManifests).Error; err != nil || activeManifests != 0 {
		t.Fatal("unknown writer activated a manifest")
	}
	rcloneTaskRequireFailedRunUnchanged(t, fixture.db, run)
}

func assertRcloneUnknownRestart(t *testing.T, fixture *rcloneHTTPFixture, binary, managed, pointID string, runID uint) {
	t.Helper()
	before := rcloneTaskPoint(t, fixture.db, pointID)
	leases := rcloneTaskLeases(t, fixture.db, pointID)
	var run model.TaskRun
	if err := fixture.db.First(&run, runID).Error; err != nil {
		t.Fatal(err)
	}
	attempt, _ := rcloneTaskAttempt(t, before)
	attemptControl := rcloneTaskControl(t, managed, attempt, "attempt.json")
	commitPath := filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "control", "commit.json")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := fixture.runtime.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("stop runtime before restart: %v", err)
	}
	cancel()
	var restartFault *rcloneTaskFaultTransport
	dependencies := backupruntime.Dependencies{DB: fixture.db, Settings: fixture.setting, SessionRevocations: fixture.jwt, Now: fixture.clock.Now,
		ToolBinaries: provider.ToolBinaries{Rclone: binary}, Metrics: publication.NoopMetrics{}}
	dependencies.Transport, dependencies.StreamTransport, dependencies.StagedPayload, dependencies.RcloneNativePreflightFactory, dependencies.RcloneNativeFactoryBuilder = rcloneTaskFaultDependencies(t, binary, &restartFault, true)(fixture.db, fixture.clock)
	restarted, err := backupruntime.New(dependencies)
	if err != nil {
		t.Fatalf("construct restarted runtime: %v", err)
	}
	fixture.runtime = restarted
	if err := restarted.SetInterruptedRunReadiness(rcloneHTTPReadiness{}); err != nil {
		t.Fatal(err)
	}
	startupErr := restarted.StartupPass(context.Background())
	if startupErr != nil && !errors.Is(startupErr, backupasset.ErrConflict) {
		t.Fatalf("restart returned an unexpected error: class=%s", rcloneHTTPSafeErrorClass(startupErr))
	}
	requested, err := restarted.FoundationService().FeatureEnabled()
	if err != nil || !requested {
		t.Fatal("restart lost the persisted feature-enabled request")
	}
	report, err := restarted.Readiness(context.Background())
	if err != nil {
		t.Fatalf("read restarted readiness: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	gateErr := ga.EvaluateEnablement(report.Snapshot)
	live, liveErr := restarted.FeatureLive()
	if liveErr != nil {
		t.Fatalf("read restarted feature gate: class=%s", rcloneHTTPSafeErrorClass(liveErr))
	}
	startupClass, readinessClass := "blocked", "ready"
	if startupErr == nil {
		startupClass = "core_started_feature_disabled"
		if live || (!errors.Is(gateErr, ga.ErrEnablementBlocked) && !errors.Is(gateErr, ga.ErrEnablementAckRequired)) {
			t.Fatalf("restart reported success with managed feature ready: requested=%t live=%t readiness=%s/%s", requested, live, report.Snapshot.Class, report.Snapshot.Status)
		}
		if errors.Is(gateErr, ga.ErrEnablementAckRequired) {
			readinessClass = "ack_required"
		} else {
			readinessClass = "blocked"
		}
	} else if gateErr != nil {
		t.Fatalf("restart failed before proving managed readiness: class=%s", rcloneHTTPSafeErrorClass(gateErr))
	}
	unresolvedChecker, ok := restarted.PublicationReconciler().(interface {
		HasUnresolvedPublication(context.Context) (bool, error)
	})
	if !ok {
		t.Fatal("restarted publication readiness is unavailable")
	}
	unresolved, err := unresolvedChecker.HasUnresolvedPublication(context.Background())
	if err != nil || !unresolved {
		t.Fatal("restart did not retain the unresolved publication guard")
	}
	repeated, err := restarted.PublicationReconciler().ProcessPoint(context.Background(), pointID)
	if err != nil || repeated.State != backupasset.RecoveryPointPreparing || repeated.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatal("restarted runtime changed the expired unknown hold")
	}
	after := rcloneTaskPoint(t, fixture.db, pointID)
	if after.State != before.State || after.ConsistencyJSON != before.ConsistencyJSON || after.EncryptedProviderLocator != before.EncryptedProviderLocator ||
		after.LineageJSON != before.LineageJSON || after.PointRevision != before.PointRevision || !after.UpdatedAt.Equal(before.UpdatedAt) ||
		!reflect.DeepEqual(leases, rcloneTaskLeases(t, fixture.db, pointID)) || len(restartFault.snapshot()) != 0 {
		t.Fatal("restart changed an expired unknown hold or contacted the provider")
	}
	rcloneTaskRequireFailedRunUnchanged(t, fixture.db, run)
	if !bytes.Equal(attemptControl, rcloneTaskControl(t, managed, attempt, "attempt.json")) {
		t.Fatal("restart changed the signed remote attempt")
	}
	if _, err := os.Stat(commitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restart changed the absent remote commit")
	}
	var taskEntity model.Task
	if err := fixture.db.First(&taskEntity, run.TaskID).Error; err != nil {
		t.Fatal(err)
	}
	started := fixture.clock.Now()
	probeRun := model.TaskRun{TaskID: run.TaskID, NodeIDSnapshot: run.NodeIDSnapshot, ExecutorTypeSnapshot: "rclone",
		TriggerType: "manual", Status: model.TaskRunStatusRunning, StartedAt: &started}
	if err := fixture.db.Create(&probeRun).Error; err != nil {
		t.Fatal(err)
	}
	audit, err := backupasset.NewSystemPublicationAuditContext("rclone-restart-writer-probe")
	if err != nil {
		t.Fatal(err)
	}
	execution, admissionErr := restarted.PublicationCoordinator().Prepare(context.Background(), publication.Run{
		Task: taskEntity, TaskRunID: probeRun.ID, Trigger: "manual", StartedAt: started, Audit: audit,
	})
	if execution != nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
		t.Fatal("restarted runtime admitted a conflicting writer")
	}
	admissionClass := "unresolved_writer"
	if startupErr == nil {
		admissionClass = "disabled_legacy_fallback_blocked"
		if !errors.Is(admissionErr, backupasset.ErrForbidden) || !strings.Contains(admissionErr.Error(), string(backupasset.FailureLegacyFallbackBlocked)) {
			t.Fatalf("disabled restart did not reject the managed writer through the admission path: class=%s", rcloneHTTPSafeErrorClass(admissionErr))
		}
	} else if !errors.Is(admissionErr, backupasset.ErrPublicationInProgress) {
		t.Fatalf("managed restart did not reject the competing writer: class=%s", rcloneHTTPSafeErrorClass(admissionErr))
	}
	var probePoints int64
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("producing_task_run_id = ?", probeRun.ID).Count(&probePoints).Error; err != nil || probePoints != 0 {
		t.Fatal("rejected restart writer reserved another recovery point")
	}
	var originalRun model.TaskRun
	if err := fixture.db.First(&originalRun, run.ID).Error; err != nil || originalRun.Status != run.Status || originalRun.LastError != run.LastError ||
		originalRun.FinishedAt == nil || run.FinishedAt == nil || !originalRun.FinishedAt.Equal(*run.FinishedAt) {
		t.Fatal("admission probe changed the original failed TaskRun")
	}
	var retryEffects int64
	if err := fixture.db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeRetry).Count(&retryEffects).Error; err != nil || retryEffects != 0 {
		t.Fatal("admission probe created a retry effect")
	}
	finalPoint := rcloneTaskPoint(t, fixture.db, pointID)
	if finalPoint.ConsistencyJSON != before.ConsistencyJSON || finalPoint.EncryptedProviderLocator != before.EncryptedProviderLocator || finalPoint.LineageJSON != before.LineageJSON ||
		!reflect.DeepEqual(leases, rcloneTaskLeases(t, fixture.db, pointID)) || len(restartFault.snapshot()) != 0 {
		t.Fatal("restart admission probe changed the hold or contacted the provider")
	}
	if !bytes.Equal(attemptControl, rcloneTaskControl(t, managed, attempt, "attempt.json")) {
		t.Fatal("admission probe changed the signed remote attempt")
	}
	if _, err := os.Stat(commitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("admission probe created a remote commit")
	}
	t.Logf("restart gate: requested=%t startup=%s readiness=%s class=%s status=%s inventory_complete=%t export_valid=%t keys_ready=%t feature_live=%t unresolved=%t admission=%s",
		requested, startupClass, readinessClass, report.Snapshot.Class, report.Snapshot.Status, report.Snapshot.InventoryComplete,
		report.Snapshot.ExportRootValid, report.Snapshot.KeyDomainsReady, live, unresolved, admissionClass)
}

func assertRcloneRealRetry(t *testing.T, fixture *rcloneHTTPFixture, manager *task.Manager, taskID, runID uint, first model.RecoveryPoint, managed string) {
	t.Helper()
	oldAttempt, _ := rcloneTaskAttempt(t, first)
	oldRoot := filepath.Join(managed, "points", oldAttempt.Portable.AttemptComponent)
	oldMarker, err := os.ReadFile(filepath.Join(oldRoot, "control", "attempt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var effect model.TaskRunEffect
	if err := fixture.db.Where("task_run_id = ? AND effect_type = ?", runID, model.TaskRunEffectTypeRetry).First(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRunEffect{}).Where("id = ?", effect.ID).Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadSchedules(context.Background()); err != nil {
		t.Fatalf("deliver durable retry: %v", err)
	}
	var retry model.TaskRun
	var retryPoint model.RecoveryPoint
	waitRcloneTaskCondition(t, 120*time.Second, func() bool {
		if err := fixture.db.Where("task_id = ? AND trigger_type = ?", taskID, "retry").Order("id DESC").First(&retry).Error; err != nil {
			return false
		}
		if retry.Status != model.TaskRunStatusSuccess {
			return false
		}
		return fixture.db.Where("producing_task_run_id = ?", retry.ID).First(&retryPoint).Error == nil && retryPoint.State == string(backupasset.RecoveryPointVerifying)
	})
	outcome, err := fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), retryPoint.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("verify retry point: %+v %v", outcome, err)
	}
	_, _, newAttempt, _ := fixture.portableHTTPPoint(retryPoint.ID)
	newRoot := filepath.Join(managed, "points", newAttempt.Portable.AttemptComponent)
	currentMarker, err := os.ReadFile(filepath.Join(oldRoot, "control", "attempt.json"))
	if err != nil || string(currentMarker) != string(oldMarker) || retry.ID == runID || retryPoint.ID == first.ID || newAttempt.AttemptID == oldAttempt.AttemptID || newRoot == oldRoot {
		t.Fatal("retry reused an identity or changed the original attempt")
	}
}

func waitRcloneTaskCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for real TaskRun evidence")
		case <-ticker.C:
		}
	}
}

func rcloneTaskSlowBinary(t *testing.T, binary, pidFile, stoppedFile string) string {
	t.Helper()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	// The SSH command owns a real Rclone child. On TERM the supervisor kills
	// and waits for that exact child before exiting, so an SSH exit message is
	// positive evidence that the controlled writer has stopped.
	script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = copy ]; then\n" +
		"    " + quote(binary) + " --bwlimit 8k \"$@\" &\n" +
		"    child=$!\n    stop_marker=" + quote(stoppedFile) + "\n" +
		"    trap 'kill -KILL \"$child\" 2>/dev/null || :; wait \"$child\" 2>/dev/null || :; printf \"%s\\n\" stopped > \"$stop_marker\"; exit 143' TERM\n" +
		"    printf '%s\\n' \"$child\" > " + quote(pidFile) + "\n" +
		"    wait \"$child\"\n    exit $?\n  fi\ndone\nexec " + quote(binary) + " \"$@\"\n"
	path := filepath.Join(t.TempDir(), "rclone-real-throttled")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func reapRcloneTaskPID(t *testing.T, pid int, binary string) {
	t.Helper()
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("inspect owned rclone process: %v", err)
	}
	expected, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	if exe != expected {
		t.Fatal("owned rclone PID no longer identifies the test binary")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("stop owned rclone: %v", err)
	}
	waitRcloneTaskCondition(t, 10*time.Second, func() bool {
		_, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		return errors.Is(err, os.ErrNotExist)
	})
}

type rcloneTaskSSHProxy struct {
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	acceptDone  chan struct{}
	workers     sync.WaitGroup
}

func newRcloneTaskSSHProxy(t *testing.T, upstream string) *rcloneTaskSSHProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &rcloneTaskSSHProxy{listener: listener, connections: make(map[net.Conn]struct{}), acceptDone: make(chan struct{})}
	go func() {
		defer close(proxy.acceptDone)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = client.Close()
				continue
			}
			proxy.mu.Lock()
			proxy.connections[client] = struct{}{}
			proxy.connections[server] = struct{}{}
			proxy.mu.Unlock()
			proxy.workers.Add(1)
			go func() {
				defer proxy.workers.Done()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(server, client); _ = server.Close(); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, server); _ = client.Close(); done <- struct{}{} }()
				<-done
				<-done
				proxy.mu.Lock()
				delete(proxy.connections, client)
				delete(proxy.connections, server)
				proxy.mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-proxy.acceptDone; proxy.dropActive(); proxy.workers.Wait() })
	return proxy
}
func (proxy *rcloneTaskSSHProxy) port() int { return proxy.listener.Addr().(*net.TCPAddr).Port }
func (proxy *rcloneTaskSSHProxy) dropActive() {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	for connection := range proxy.connections {
		_ = connection.Close()
	}
}
