package executor

import (
	"context"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestFactoryInjectsResticPublisherOnlyIntoEvidenceLane(t *testing.T) {
	publisher := &evidencePublisher{}
	factory := NewFactoryWithResticPublisher("rsync", publisher)
	restic, ok := factory.Resolve("restic").(EvidenceExecutor)
	if !ok {
		t.Fatalf("restic executor=%T does not implement EvidenceExecutor", factory.Resolve("restic"))
	}
	for _, executorType := range []string{"rsync", "rclone", "command"} {
		if _, ok := factory.Resolve(executorType).(EvidenceExecutor); ok {
			t.Fatalf("%s executor unexpectedly implements EvidenceExecutor", executorType)
		}
	}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/data", ExecutorConfig: `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY","exclude_patterns":["cache"]}`}
	attempt := provider.PublicationAttempt{Provider: backupasset.ProviderRestic, TaskID: taskEntity.ID, TaskRunID: 9}
	result, err := restic.RunWithEvidence(context.Background(), EvidenceExecutionRequest{Task: taskEntity, TaskRunID: 9, Attempt: attempt}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero {
		t.Fatalf("evidence result=%+v", result)
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

func TestResticExecutorImplementsEvidenceExecutor(t *testing.T) {
	var _ EvidenceExecutor = (*ResticExecutor)(nil)
}

func TestResticEvidenceExecutorRejectsTaskAttemptProviderAndRunIdentityMismatch(t *testing.T) {
	publisher := &evidencePublisher{}
	executor := &ResticExecutor{publisher: publisher}
	taskEntity := model.Task{ID: 7, ExecutorType: "restic", RsyncSource: "/data"}
	valid := provider.PublicationAttempt{Provider: backupasset.ProviderRestic, TaskID: taskEntity.ID, TaskRunID: 9}
	for _, request := range []EvidenceExecutionRequest{
		{Task: taskEntity, TaskRunID: 8, Attempt: valid},
		{Task: model.Task{ID: taskEntity.ID, ExecutorType: "rsync"}, TaskRunID: 9, Attempt: valid},
		{Task: taskEntity, TaskRunID: 9, Attempt: provider.PublicationAttempt{Provider: backupasset.ProviderRsync, TaskID: taskEntity.ID, TaskRunID: 9}},
		{Task: taskEntity, TaskRunID: 9, Attempt: provider.PublicationAttempt{Provider: backupasset.ProviderRestic, TaskID: 8, TaskRunID: 9}},
	} {
		if _, err := executor.RunWithEvidence(context.Background(), request, nil, nil); err == nil {
			t.Fatalf("invalid evidence request was accepted: %+v", request)
		}
	}
	if publisher.input.Source != "" || len(publisher.input.Excludes) != 0 {
		t.Fatalf("invalid request reached Restic publisher: %+v", publisher.input)
	}
}

func TestNonEvidenceExecutorsRetainCurrentContract(t *testing.T) {
	factory := NewFactory("rsync")
	for _, executorType := range []string{"rsync", "rclone", "command"} {
		if _, ok := factory.Resolve(executorType).(EvidenceExecutor); ok {
			t.Fatalf("%s unexpectedly implements the Restic-only evidence contract", executorType)
		}
	}
}

type evidencePublisher struct {
	input provider.ResticBackupInput
}

func (publisher *evidencePublisher) Backup(_ context.Context, _ provider.PublicationAttempt, input provider.ResticBackupInput, _ func(provider.ResticBackupProgress)) (provider.ResticBackupResult, error) {
	publisher.input = input
	return provider.ResticBackupResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero}, nil
}

func (*evidencePublisher) LookupAttempt(context.Context, provider.PublicationAttempt) ([]provider.ResticSnapshotObservation, error) {
	return nil, nil
}
