package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func TestProviderRunnerEvidenceUsesExactTaskRunAttempt(t *testing.T) {
	attempt := publicationResticAttempt(7, 9)
	commit := provider.NewResticProviderCommit(provider.ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: "restic:v1:test", NativePointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CaptureStartedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
	})
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt}
	coordinator := &publicationCoordinatorFake{execution: session}
	evidence := &evidenceExecutorFake{result: executor.PublicationExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}
	manager := &Manager{executorFactory: executorFactoryFake{executor: evidence}, publicationCoordinator: coordinator}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/source"}

	result := manager.executeProvider(context.Background(), taskEntity, 9, "manual", "", nil, nil)
	if result.Err != nil || result.ExitCode != 0 || !result.Managed || result.SuppressRetry {
		t.Fatalf("provider result=%+v", result)
	}
	if coordinator.run.TaskRunID != 9 || coordinator.run.Task.ID != taskEntity.ID || evidence.request.TaskRunID != 9 ||
		evidence.request.Attempt.Restic == nil || evidence.request.Attempt.Restic.TaskID != attempt.Restic.TaskID || evidence.request.Attempt.Restic.TaskRunID != attempt.Restic.TaskRunID || evidence.request.Attempt.Restic.RecoveryPointID != attempt.Restic.RecoveryPointID {
		t.Fatalf("coordinator run=%+v evidence request=%+v", coordinator.run, evidence.request)
	}
	if session.commit == nil || *session.commit != commit || session.deferCall != nil || session.failCode != "" || session.rejectCode != "" {
		t.Fatalf("publication session=%+v", session)
	}
}

func TestTaskRunSuccessAndPublicationFailureRemainIndependentFacts(t *testing.T) {
	attempt := publicationResticAttempt(7, 9)
	commit := provider.NewResticProviderCommit(provider.ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: "restic:v1:test", NativePointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CaptureStartedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
	})
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt, recordErr: errors.New("FAKE_PUBLICATION_DURABILITY_FAILURE_FOR_TEST_ONLY")}
	manager := &Manager{
		executorFactory:        executorFactoryFake{executor: &evidenceExecutorFake{result: executor.PublicationExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}},
		publicationCoordinator: &publicationCoordinatorFake{execution: session},
	}

	result := manager.executeProvider(context.Background(), model.Task{ID: attempt.Restic.TaskID, ExecutorType: "restic", RsyncSource: "/source"}, attempt.Restic.TaskRunID, "manual", "", nil, nil)
	if result.ExitCode != 0 || result.Err != nil || !result.Managed || result.WarningCode != backupasset.FailurePublicationSessionAbandoned {
		t.Fatalf("successful provider transfer was changed by publication failure: %+v", result)
	}
	if session.abandonCalls != 1 {
		t.Fatalf("durability failure left publication session open: abandons=%d", session.abandonCalls)
	}
}

func TestProviderRunnerRoutesManagedRsyncThroughBoundPublicationInput(t *testing.T) {
	attempt := publicationRsyncAttempt(7, 9)
	commit := provider.NewRsyncTreeProviderCommit(provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: attempt.RsyncTree.RepositoryID, TaskRepositoryLinkID: attempt.RsyncTree.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RsyncTree.RecoveryPointID, AttemptID: attempt.RsyncTree.AttemptID, PublicationMode: attempt.RsyncTree.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("a", 64), ManifestEntryCount: 1, LogicalBytes: 1,
		FidelityDigest: strings.Repeat("b", 64), SourceFingerprint: strings.Repeat("c", 64), ProviderCommittedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		CommitMarkerDigest: strings.Repeat("d", 64), ChildFenceDigest: strings.Repeat("e", 64), PointDeadlineAt: attempt.RsyncTree.PointDeadlineAt,
		RenameVerified: true, DirectoryFsyncVerified: true,
	})
	input := provider.RsyncTreePublicationInput{
		ManagedRoot: "/managed-root", MarkerKey: []byte("FAKE_RSYNC_MARKER_KEY_FOR_RUNNER_TEST"), SourceFingerprint: strings.Repeat("c", 64), ChildFenceDigest: strings.Repeat("e", 64),
		ManifestLimits: provider.ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 10, MaxRecordBytes: 1024, MaxDepth: 8}, MaxCommandOutputBytes: 1 << 20,
	}
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt, rsyncInput: &input}
	evidence := &evidenceExecutorFake{result: executor.PublicationExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}
	manager := &Manager{executorFactory: executorFactoryFake{executor: evidence}, publicationCoordinator: &publicationCoordinatorFake{execution: session}}
	taskEntity := model.Task{ID: 7, ExecutorType: "rsync", RsyncSource: "/source", RsyncTarget: "/legacy-target"}

	result := manager.executeProvider(context.Background(), taskEntity, 9, "manual", "", nil, nil)
	if result.Err != nil || result.ExitCode != 0 || !result.Managed || result.WarningCode != "" {
		t.Fatalf("managed Rsync provider result=%+v", result)
	}
	if evidence.request.RsyncTreeInput == nil || evidence.request.RsyncTreeInput.ManagedRoot != input.ManagedRoot ||
		evidence.request.Attempt.RsyncTree == nil || evidence.request.Attempt.RsyncTree.RecoveryPointID != attempt.RsyncTree.RecoveryPointID ||
		session.commit == nil || *session.commit != commit {
		t.Fatalf("managed Rsync request=%+v session=%+v", evidence.request, session)
	}
}

func TestManagedProviderResultBypassesLegacyVerifier(t *testing.T) {
	policy := &model.Policy{VerifyEnabled: true}
	if shouldRunLegacyVerification(providerRunResult{Managed: true}, policy) {
		t.Fatal("managed provider result enabled mutable legacy verifier")
	}
	if !shouldRunLegacyVerification(providerRunResult{}, policy) {
		t.Fatal("legacy provider result unexpectedly bypassed verifier")
	}
	if shouldRunLegacyVerification(providerRunResult{}, nil) {
		t.Fatal("missing policy enabled verifier")
	}
}

type executorFactoryFake struct{ executor executor.Executor }

func (factory executorFactoryFake) Resolve(string) executor.Executor { return factory.executor }

type evidenceExecutorFake struct {
	request executor.PublicationExecutionRequest
	result  executor.PublicationExecutionResult
	err     error
}

func (fake *evidenceExecutorFake) Run(context.Context, model.Task, executor.LogFunc, executor.ProgressFunc) (int, error) {
	return -1, nil
}

func (fake *evidenceExecutorFake) RunWithPublication(_ context.Context, request executor.PublicationExecutionRequest, _ executor.LogFunc, _ executor.ProgressFunc) (executor.PublicationExecutionResult, error) {
	fake.request = request
	return fake.result, fake.err
}

type publicationCoordinatorFake struct {
	run       publication.Run
	execution publication.Execution
	err       error
}

func (fake *publicationCoordinatorFake) Prepare(_ context.Context, run publication.Run) (publication.Execution, error) {
	fake.run = run
	return fake.execution, fake.err
}

type publicationExecutionFake struct {
	mode         publication.ExecutionMode
	attempt      provider.TaggedPublicationAttempt
	commit       *provider.ProviderCommit
	deferCall    *publication.Deferral
	failCode     backupasset.PublicationFailureCode
	rejectCode   backupasset.PublicationFailureCode
	recordErr    error
	abandonCalls int
	rsyncInput   *provider.RsyncTreePublicationInput
}

func (fake *publicationExecutionFake) Mode() publication.ExecutionMode { return fake.mode }
func (fake *publicationExecutionFake) Attempt() *provider.TaggedPublicationAttempt {
	copy := fake.attempt
	return &copy
}
func (*publicationExecutionFake) Context() context.Context { return context.Background() }
func (*publicationExecutionFake) Cancel(error) error       { return nil }
func (fake *publicationExecutionFake) Abandon(error) error {
	fake.abandonCalls++
	return nil
}
func (*publicationExecutionFake) CompleteCompatibility(context.Context) error {
	return nil
}
func (fake *publicationExecutionFake) RsyncTreePublicationInput() (provider.RsyncTreePublicationInput, error) {
	if fake.rsyncInput == nil {
		return provider.RsyncTreePublicationInput{}, errors.New("FAKE_RSYNC_PUBLICATION_INPUT_NOT_CONFIGURED")
	}
	copy := *fake.rsyncInput
	copy.MarkerKey = append([]byte(nil), copy.MarkerKey...)
	return copy, nil
}
func (fake *publicationExecutionFake) RecordProviderCommit(_ context.Context, commit provider.ProviderCommit) (publication.Outcome, error) {
	copy := commit
	fake.commit = &copy
	if fake.recordErr != nil {
		return publication.Outcome{}, fake.recordErr
	}
	pointID := ""
	if fake.attempt.Restic != nil {
		pointID = fake.attempt.Restic.RecoveryPointID
	} else if fake.attempt.RsyncTree != nil {
		pointID = fake.attempt.RsyncTree.RecoveryPointID
	}
	return publication.Outcome{RecoveryPointID: pointID, State: backupasset.RecoveryPointVerifying, ProviderCommitRecorded: true}, nil
}
func (fake *publicationExecutionFake) Defer(_ context.Context, deferral publication.Deferral) error {
	copy := deferral
	fake.deferCall = &copy
	return nil
}
func (fake *publicationExecutionFake) Reject(_ context.Context, code backupasset.PublicationFailureCode) error {
	fake.rejectCode = code
	return nil
}
func (fake *publicationExecutionFake) Fail(_ context.Context, code backupasset.PublicationFailureCode) error {
	fake.failCode = code
	return nil
}

var _ publication.Coordinator = (*publicationCoordinatorFake)(nil)
var _ publication.Execution = (*publicationExecutionFake)(nil)
var _ executor.PublicationExecutor = (*evidenceExecutorFake)(nil)

func publicationResticAttempt(taskID, taskRunID uint) provider.TaggedPublicationAttempt {
	return provider.NewResticPublicationAttempt(provider.ResticAttemptV1{
		Provider:             backupasset.ProviderRestic,
		RepositoryID:         strings.Repeat("a", 32),
		RepositoryIdentity:   provider.NativeResticIdentityPrefix + strings.Repeat("f", 64),
		TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID:      strings.Repeat("c", 32),
		TaskID:               taskID,
		TaskRunID:            taskRunID,
		RequiredTags:         [2]string{"xirang.link.v1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "xirang.point.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		PointDeadlineAt:      time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC),
		CapabilityRevision:   1,
		AdapterRevision:      "restic-v1",
	})
}

func publicationRsyncAttempt(taskID, taskRunID uint) provider.TaggedPublicationAttempt {
	pointID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("b", 32)
	return provider.NewRsyncTreePublicationAttempt(provider.RsyncTreeAttemptV1{
		RepositoryID: strings.Repeat("c", 32), TaskRepositoryLinkID: strings.Repeat("d", 32), RecoveryPointID: pointID, AttemptID: attemptID,
		TaskID: taskID, TaskRunID: taskRunID, PublicationMode: backupasset.PublicationVersionedFullCopy,
		PointDeadlineAt: time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC), ExpectedTaskRevision: 1,
		RepositoryMarkerDigest: strings.Repeat("e", 64), ManagedRootIdentityDigest: strings.Repeat("f", 64),
		StagingComponent: pointID + "." + attemptID, FinalComponent: pointID, CommandProfileVersion: 1,
		PreflightID: strings.Repeat("1", 32), PreflightDigest: strings.Repeat("2", 64),
	})
}
