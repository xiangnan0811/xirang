package task

import (
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

// This consumer regression deliberately uses only contracts present in the
// baseline, so the same test can be overlaid on the original candidate.
func TestRcloneUnclassifiedFailureNeverBecomesPrecommandRejection(t *testing.T) {
	attempt := publicationRcloneAttempt(7, 9)
	input := provider.RclonePublicationInput{PortableRequest: &provider.RclonePortablePublicationRequest{Attempt: *attempt.Rclone}}
	session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt, rcloneInput: &input}
	worker := &evidenceExecutorFake{result: executor.PublicationExecutionResult{}, err: provider.ErrRcloneCommitOutcomeUnknown}
	manager := &Manager{executorFactory: executorFactoryFake{executor: worker}, publicationCoordinator: &publicationCoordinatorFake{execution: session}}
	result := manager.executeProvider(context.Background(), model.Task{ID: 7, ExecutorType: "rclone", RsyncSource: "/source"}, 9, "manual", "", nil, nil, nil)
	if !errors.Is(result.Err, provider.ErrRcloneCommitOutcomeUnknown) || !result.SuppressRetry || result.ExitCode != provider.UnknownProviderExitCode ||
		session.rejectCode != "" || session.deferCall == nil || session.deferCall.Completion != backupasset.CompletionOutcomeUnknown {
		t.Fatalf("unknown publication was rejected or made retryable: result=%+v deferral=%+v rejection=%q", result, session.deferCall, session.rejectCode)
	}
}
