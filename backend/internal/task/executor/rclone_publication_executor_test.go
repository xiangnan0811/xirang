package executor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestManagedRclonePublicationExecutorDerivesOnlyTaskSource(t *testing.T) {
	strategy := &rclonePublicationStrategyFake{}
	factory := NewFactoryWithPublicationStrategies("rsync", nil, nil, strategy)
	managed, ok := factory.Resolve("rclone").(PublicationExecutor)
	if !ok {
		t.Fatalf("managed Rclone executor=%T does not implement PublicationExecutor", factory.Resolve("rclone"))
	}
	attemptValue := validRcloneExecutorAttempt()
	attempt := provider.NewRclonePublicationAttempt(attemptValue)
	input := provider.RclonePublicationInput{
		ManifestLimits: provider.ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 32},
		PortableRequest: &provider.RclonePortablePublicationRequest{
			Attempt: attemptValue, Runtime: provider.RemoteCommandAccess{Node: model.Node{ID: 9}},
		},
	}
	taskEntity := model.Task{ID: 7, ExecutorType: "rclone", RsyncSource: "/srv/source", NodeID: 9, Node: model.Node{ID: 9}}
	result, err := managed.RunWithPublication(context.Background(), PublicationExecutionRequest{
		Task: taskEntity, TaskRunID: 8, Attempt: attempt, RcloneInput: &input,
	}, nil, nil)
	if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		t.Fatalf("managed Rclone result=%+v err=%v", result, err)
	}
	wantSource, err := provider.NewRclonePrivateLocator("/srv/source")
	if err != nil {
		t.Fatal(err)
	}
	if strategy.input.PortableRequest == nil || !reflect.DeepEqual(strategy.input.PortableRequest.Source, wantSource) ||
		strategy.input.PortableRequest.Runtime.Node.ID != 9 {
		t.Fatalf("managed Rclone strategy input=%+v", strategy.input.PortableRequest)
	}

	poisoned := input
	poisoned.PortableRequest = cloneRclonePortableRequestForExecutorTest(*input.PortableRequest)
	poisoned.PortableRequest.Source = wantSource
	if _, err := managed.RunWithPublication(context.Background(), PublicationExecutionRequest{
		Task: taskEntity, TaskRunID: 8, Attempt: attempt, RcloneInput: &poisoned,
	}, nil, nil); err == nil {
		t.Fatal("managed Rclone executor accepted a caller-injected source")
	}
}

func TestRcloneFactoryKeepsLegacyAndManagedLanesSeparate(t *testing.T) {
	if _, ok := NewFactory("rsync").Resolve("rclone").(PublicationExecutor); ok {
		t.Fatal("legacy Rclone executor unexpectedly implements PublicationExecutor")
	}
	strategy := &rclonePublicationStrategyFake{}
	managed := NewFactoryWithPublicationStrategies("rsync", nil, nil, strategy).Resolve("rclone")
	if _, ok := managed.(PublicationExecutor); !ok {
		t.Fatalf("managed Rclone executor=%T", managed)
	}
	if _, ok := managed.(RestoreExecutor); !ok {
		t.Fatalf("managed Rclone wrapper lost legacy restore contract: %T", managed)
	}
}

func validRcloneExecutorAttempt() provider.RcloneAttemptV1 {
	preparedAt := time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("c", 32)
	attemptID := strings.Repeat("d", 32)
	return provider.RcloneAttemptV1{
		SchemaVersion: 1, LayoutVersion: 1, MinimumRuntimeRevision: 1, Provider: backupasset.ProviderRclone,
		RepositoryID: strings.Repeat("a", 32), TaskRepositoryLinkID: strings.Repeat("b", 32),
		RecoveryPointID: pointID, AttemptID: attemptID, TaskID: 7, TaskRunID: 8, Trigger: "manual",
		PublicationMode: backupasset.PublicationVersionedPrefix, CaptureStartedAt: preparedAt.Add(-time.Second),
		PreparedAt: preparedAt, PointDeadlineAt: preparedAt.Add(time.Hour), ExpectedTaskRevision: 1,
		BindingRevision: 2, ConfigRevision: 3, ConfigDigest: strings.Repeat("1", 64), CapabilityRevision: 4,
		CredentialRevision: 5, PreflightID: strings.Repeat("e", 32), PreflightRevision: 6,
		PreflightDigest: strings.Repeat("2", 64), ManifestSchemaRevision: 1, ManifestLimitsRevision: 1,
		ManifestLimitsDigest: strings.Repeat("3", 64), RepositoryIdentityDigest: strings.Repeat("4", 64),
		ManagedRootIdentityDigest: strings.Repeat("5", 64), ChildFenceDigest: strings.Repeat("6", 64),
		LegacyOriginEvidenceDigest: strings.Repeat("7", 64),
		Portable: &provider.RclonePortableAttemptV1{
			AttemptComponent: pointID + "." + attemptID, DataComponent: "data", ControlComponent: "control",
			AttemptMarkerDigest:      strings.Repeat("8", 64),
			ExpectedConsistencyClass: string(backupasset.RcloneConsistencyObservationallyStable),
			ExpectedHashFidelity:     string(backupasset.RcloneHashDownloadVerifiedBytes),
		},
	}
}

func cloneRclonePortableRequestForExecutorTest(value provider.RclonePortablePublicationRequest) *provider.RclonePortablePublicationRequest {
	copy := value
	return &copy
}

type rclonePublicationStrategyFake struct {
	input provider.RclonePublicationInput
}

func (*rclonePublicationStrategyFake) Kind() backupasset.ProviderKind {
	return backupasset.ProviderRclone
}

func (fake *rclonePublicationStrategyFake) Prepare(_ context.Context, request provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
	fake.input = *request.RcloneInput
	return provider.PreparedPublication{Attempt: request.Attempt, RcloneInput: request.RcloneInput}, nil
}

func (*rclonePublicationStrategyFake) Execute(_ context.Context, prepared provider.PreparedPublication, _ provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
	commit := provider.NewRcloneProviderCommit(provider.RcloneCommitV1{})
	return provider.ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &commit}, nil
}

func (*rclonePublicationStrategyFake) RecordCommit(_ context.Context, _ provider.PreparedPublication, result provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
	return *result.ProviderCommit, nil
}

func (*rclonePublicationStrategyFake) VerifyOrBuildManifest(context.Context, provider.PreparedPublication, provider.ProviderCommit, provider.ManifestLimits) (provider.ManifestResult, error) {
	return provider.ManifestResult{}, nil
}

func (*rclonePublicationStrategyFake) Reconcile(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
	return provider.PublicationReconcileResult{}, nil
}

var _ provider.PublicationStrategy = (*rclonePublicationStrategyFake)(nil)
