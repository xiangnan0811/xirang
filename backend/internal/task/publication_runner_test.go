package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

func TestProviderRunnerEvidenceUsesExactTaskRunAttempt(t *testing.T) {
	attempt := provider.PublicationAttempt{
		Provider: backupasset.ProviderRestic, TaskID: 7, TaskRunID: 9,
		RecoveryPointID: "11111111111111111111111111111111",
	}
	commit := provider.ProviderCommitEvidence{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: "restic:v1:test", NativePointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CaptureStartedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
	}
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt}
	coordinator := &publicationCoordinatorFake{execution: session}
	evidence := &evidenceExecutorFake{result: executor.EvidenceExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}
	manager := &Manager{executorFactory: executorFactoryFake{executor: evidence}, publicationCoordinator: coordinator}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/source"}

	result := manager.executeProvider(context.Background(), taskEntity, 9, "manual", "", nil, nil)
	if result.Err != nil || result.ExitCode != 0 || !result.Managed || result.SuppressRetry {
		t.Fatalf("provider result=%+v", result)
	}
	if coordinator.run.TaskRunID != 9 || coordinator.run.Task.ID != taskEntity.ID || evidence.request.TaskRunID != 9 ||
		evidence.request.Attempt.TaskID != attempt.TaskID || evidence.request.Attempt.TaskRunID != attempt.TaskRunID || evidence.request.Attempt.RecoveryPointID != attempt.RecoveryPointID {
		t.Fatalf("coordinator run=%+v evidence request=%+v", coordinator.run, evidence.request)
	}
	if session.commit == nil || *session.commit != commit || session.deferCall != nil || session.failCode != "" || session.rejectCode != "" {
		t.Fatalf("publication session=%+v", session)
	}
}

func TestTaskRunSuccessAndPublicationFailureRemainIndependentFacts(t *testing.T) {
	attempt := provider.PublicationAttempt{
		Provider: backupasset.ProviderRestic, TaskID: 7, TaskRunID: 9,
		RecoveryPointID: "11111111111111111111111111111111",
	}
	commit := provider.ProviderCommitEvidence{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: "restic:v1:test", NativePointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CaptureStartedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
	}
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt, recordErr: errors.New("FAKE_PUBLICATION_DURABILITY_FAILURE_FOR_TEST_ONLY")}
	manager := &Manager{
		executorFactory:        executorFactoryFake{executor: &evidenceExecutorFake{result: executor.EvidenceExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}}},
		publicationCoordinator: &publicationCoordinatorFake{execution: session},
	}

	result := manager.executeProvider(context.Background(), model.Task{ID: attempt.TaskID, ExecutorType: "restic", RsyncSource: "/source"}, attempt.TaskRunID, "manual", "", nil, nil)
	if result.ExitCode != 0 || result.Err != nil || !result.Managed || result.WarningCode != backupasset.FailurePublicationSessionAbandoned {
		t.Fatalf("successful provider transfer was changed by publication failure: %+v", result)
	}
	if session.abandonCalls != 1 {
		t.Fatalf("durability failure left publication session open: abandons=%d", session.abandonCalls)
	}
}

type executorFactoryFake struct{ executor executor.Executor }

func (factory executorFactoryFake) Resolve(string) executor.Executor { return factory.executor }

type evidenceExecutorFake struct {
	request executor.EvidenceExecutionRequest
	result  executor.EvidenceExecutionResult
	err     error
}

func (fake *evidenceExecutorFake) Run(context.Context, model.Task, executor.LogFunc, executor.ProgressFunc) (int, error) {
	return -1, nil
}

func (fake *evidenceExecutorFake) RunWithEvidence(_ context.Context, request executor.EvidenceExecutionRequest, _ executor.LogFunc, _ executor.ProgressFunc) (executor.EvidenceExecutionResult, error) {
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
	attempt      provider.PublicationAttempt
	commit       *provider.ProviderCommitEvidence
	deferCall    *publication.Deferral
	failCode     backupasset.PublicationFailureCode
	rejectCode   backupasset.PublicationFailureCode
	recordErr    error
	abandonCalls int
}

func (fake *publicationExecutionFake) Mode() publication.ExecutionMode { return fake.mode }
func (fake *publicationExecutionFake) Attempt() *provider.PublicationAttempt {
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
func (fake *publicationExecutionFake) RecordProviderCommit(_ context.Context, commit provider.ProviderCommitEvidence) (publication.Outcome, error) {
	copy := commit
	fake.commit = &copy
	if fake.recordErr != nil {
		return publication.Outcome{}, fake.recordErr
	}
	return publication.Outcome{RecoveryPointID: fake.attempt.RecoveryPointID, State: backupasset.RecoveryPointVerifying, ProviderCommitRecorded: true}, nil
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
var _ executor.EvidenceExecutor = (*evidenceExecutorFake)(nil)
