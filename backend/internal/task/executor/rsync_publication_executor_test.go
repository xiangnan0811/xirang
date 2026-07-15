package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestFactoryRoutesManagedRsyncAttemptThroughTypedStrategy(t *testing.T) {
	attempt := managedRsyncAttemptForExecutorTest(7, 9)
	commit := provider.NewRsyncTreeProviderCommit(provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: attempt.RsyncTree.RepositoryID, TaskRepositoryLinkID: attempt.RsyncTree.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RsyncTree.RecoveryPointID, AttemptID: attempt.RsyncTree.AttemptID, PublicationMode: attempt.RsyncTree.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("a", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("b", 64), SourceFingerprint: strings.Repeat("c", 64), ProviderCommittedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		CommitMarkerDigest: strings.Repeat("d", 64), ChildFenceDigest: strings.Repeat("e", 64), PointDeadlineAt: attempt.RsyncTree.PointDeadlineAt,
		RenameVerified: true, DirectoryFsyncVerified: true,
	})
	strategy := &rsyncPublicationStrategyFake{result: provider.ProviderExecutionResult{
		ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit,
	}}
	factory := NewFactoryWithPublicationStrategies("rsync", nil, strategy)
	managed, ok := factory.Resolve("rsync").(PublicationExecutor)
	if !ok {
		t.Fatalf("managed rsync executor=%T does not implement PublicationExecutor", factory.Resolve("rsync"))
	}
	taskEntity := model.Task{ID: 7, ExecutorType: "rsync", RsyncSource: "/source", RsyncTarget: "/legacy-mutable-target"}
	input := provider.RsyncTreePublicationInput{
		ManagedRoot: t.TempDir(), CaptureACLs: true, CaptureXattrs: true, MarkerKey: []byte("FAKE_RSYNC_MARKER_KEY_FOR_EXECUTOR_TEST"),
		SourceFingerprint: strings.Repeat("c", 64), ChildFenceDigest: strings.Repeat("e", 64),
		ManifestLimits: provider.ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 1024, MaxDepth: 32}, MaxCommandOutputBytes: 1 << 20,
	}
	result, err := managed.RunWithPublication(context.Background(), PublicationExecutionRequest{Task: taskEntity, TaskRunID: 9, Attempt: attempt, RsyncTreeInput: &input}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		t.Fatalf("managed rsync execution result=%+v", result)
	}
	if strategy.input.Source.LocalPath != taskEntity.RsyncSource || strategy.input.Source.Remote != nil || strategy.input.ManagedRoot != input.ManagedRoot ||
		strategy.input.Source.LocalPath == taskEntity.RsyncTarget || strategy.prepare.Attempt != attempt {
		t.Fatalf("managed rsync strategy input=%+v prepare=%+v", strategy.input, strategy.prepare)
	}
}

func TestManagedRsyncPublicationExecutorRejectsMismatchedRequest(t *testing.T) {
	strategy := &rsyncPublicationStrategyFake{}
	executor := &RsyncPublicationExecutor{legacy: &RsyncExecutor{binary: "rsync"}, strategy: strategy}
	valid := managedRsyncAttemptForExecutorTest(7, 9)
	for _, request := range []PublicationExecutionRequest{
		{Task: model.Task{ID: 7, ExecutorType: "rsync"}, TaskRunID: 8, Attempt: valid},
		{Task: model.Task{ID: 7, ExecutorType: "restic"}, TaskRunID: 9, Attempt: valid},
		{Task: model.Task{ID: 7, ExecutorType: "rsync"}, TaskRunID: 9, Attempt: provider.NewResticPublicationAttempt(provider.ResticAttemptV1{})},
		{Task: model.Task{ID: 7, ExecutorType: "rsync"}, TaskRunID: 9, Attempt: valid},
	} {
		if _, err := executor.RunWithPublication(context.Background(), request, nil, nil); err == nil {
			t.Fatalf("invalid managed Rsync request was accepted: %+v", request)
		}
	}
	if strategy.prepare.Attempt.Provider != "" {
		t.Fatalf("invalid managed Rsync request reached provider strategy: %+v", strategy.prepare)
	}
}

type rsyncPublicationStrategyFake struct {
	prepare provider.PublicationPrepareRequest
	input   provider.RsyncTreePublicationInput
	result  provider.ProviderExecutionResult
}

func (*rsyncPublicationStrategyFake) Kind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}

func (fake *rsyncPublicationStrategyFake) Prepare(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
	fake.prepare = request
	if request.RsyncTreeInput != nil {
		fake.input = *request.RsyncTreeInput
	}
	return provider.PreparedPublication{Attempt: request.Attempt, RsyncTreeInput: request.RsyncTreeInput}, nil
}

func (fake *rsyncPublicationStrategyFake) Execute(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
	return fake.result, nil
}

func (fake *rsyncPublicationStrategyFake) RecordCommit(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
	if result.ProviderCommit == nil {
		return provider.ProviderCommit{}, nil
	}
	return *result.ProviderCommit, nil
}

func (*rsyncPublicationStrategyFake) VerifyOrBuildManifest(context.Context, provider.PreparedPublication, provider.ProviderCommit, provider.ManifestLimits) (provider.ManifestResult, error) {
	return provider.ManifestResult{}, nil
}

func (*rsyncPublicationStrategyFake) Reconcile(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
	return provider.PublicationReconcileResult{}, nil
}

func managedRsyncAttemptForExecutorTest(taskID, taskRunID uint) provider.TaggedPublicationAttempt {
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

var _ provider.PublicationStrategy = (*rsyncPublicationStrategyFake)(nil)
