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

func TestFactoryInjectsResticStrategyOnlyIntoPublicationLane(t *testing.T) {
	publisher := &evidencePublisher{}
	strategy, err := provider.NewResticPublicationStrategy(publisher, evidenceManifestBuilder{})
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactoryWithResticPublicationStrategy("rsync", strategy)
	restic, ok := factory.Resolve("restic").(PublicationExecutor)
	if !ok {
		t.Fatalf("restic executor=%T does not implement PublicationExecutor", factory.Resolve("restic"))
	}
	for _, executorType := range []string{"rsync", "rclone", "command"} {
		if _, ok := factory.Resolve(executorType).(PublicationExecutor); ok {
			t.Fatalf("%s executor unexpectedly implements PublicationExecutor", executorType)
		}
	}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/data", ExecutorConfig: `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY","exclude_patterns":["cache"]}`}
	attempt := evidenceResticAttempt(taskEntity.ID, 9)
	result, err := restic.RunWithPublication(context.Background(), PublicationExecutionRequest{Task: taskEntity, TaskRunID: 9, Attempt: attempt}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		t.Fatalf("evidence result=%+v", result)
	}
	if _, err := result.ProviderCommit.ResticCommit(); err != nil {
		t.Fatalf("provider commit=%+v err=%v", result.ProviderCommit, err)
	}
	if publisher.input.Source != "/data" || len(publisher.input.Excludes) != 1 || publisher.input.Excludes[0] != "cache" {
		t.Fatalf("publisher input=%+v", publisher.input)
	}
}

func TestResticEvidenceConfigIgnoresLegacyAccessSecretsAndExtractsOnlyBoundedExcludes(t *testing.T) {
	config, err := parseResticEvidenceConfig(`{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY","append_only":true,"exclude_patterns":["cache","tmp"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ExcludePatterns) != 2 || config.ExcludePatterns[0] != "cache" || config.ExcludePatterns[1] != "tmp" {
		t.Fatalf("evidence config=%+v", config)
	}
	if _, err := parseResticEvidenceConfig(`{"exclude_patterns":["bad\u0000pattern"]}`); err == nil {
		t.Fatal("NUL-bearing evidence exclude was accepted")
	}
}

func TestResticExecutorImplementsPublicationExecutor(t *testing.T) {
	var _ PublicationExecutor = (*ResticExecutor)(nil)
}

func TestResticPublicationExecutorRejectsTaskAttemptProviderAndRunIdentityMismatch(t *testing.T) {
	publisher := &evidencePublisher{}
	strategy, err := provider.NewResticPublicationStrategy(publisher, evidenceManifestBuilder{})
	if err != nil {
		t.Fatal(err)
	}
	executor := &ResticExecutor{strategy: strategy}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/data"}
	valid := evidenceResticAttempt(taskEntity.ID, 9)
	for _, request := range []PublicationExecutionRequest{
		{Task: taskEntity, TaskRunID: 8, Attempt: valid},
		{Task: model.Task{ID: taskEntity.ID, ExecutorType: "rsync"}, TaskRunID: 9, Attempt: valid},
		{Task: taskEntity, TaskRunID: 9, Attempt: provider.TaggedPublicationAttempt{Provider: backupasset.ProviderRsync, Version: 1}},
		{Task: taskEntity, TaskRunID: 9, Attempt: evidenceResticAttempt(8, 9)},
	} {
		if _, err := executor.RunWithPublication(context.Background(), request, nil, nil); err == nil {
			t.Fatalf("invalid evidence request was accepted: %+v", request)
		}
	}
	if publisher.input.Source != "" || len(publisher.input.Excludes) != 0 {
		t.Fatalf("invalid request reached Restic publisher: %+v", publisher.input)
	}
}

func TestNonPublicationExecutorsRetainCurrentContract(t *testing.T) {
	factory := NewFactory("rsync")
	for _, executorType := range []string{"rsync", "rclone", "command"} {
		if _, ok := factory.Resolve(executorType).(PublicationExecutor); ok {
			t.Fatalf("%s unexpectedly implements the managed publication contract", executorType)
		}
	}
}

type evidencePublisher struct {
	input provider.ResticBackupInput
}

func (publisher *evidencePublisher) Backup(_ context.Context, attempt provider.ResticAttemptV1, input provider.ResticBackupInput, _ func(provider.ResticBackupProgress)) (provider.ResticBackupResult, error) {
	publisher.input = input
	return provider.ResticBackupResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &provider.ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: attempt.RepositoryIdentity, NativePointID: strings.Repeat("a", 64),
		CaptureStartedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 12, 0, 1, 0, time.UTC),
	}}, nil
}

func (*evidencePublisher) LookupAttempt(context.Context, provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
	return nil, nil
}

type evidenceManifestBuilder struct{}

func (evidenceManifestBuilder) BuildManifest(context.Context, provider.ResticAttemptV1, provider.ResticCommitV1, provider.ManifestLimits) (provider.ResticManifestV1, error) {
	return provider.ResticManifestV1{}, nil
}

func evidenceResticAttempt(taskID, taskRunID uint) provider.TaggedPublicationAttempt {
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
