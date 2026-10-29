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

func TestRclonePublicationFailureReachesTaskFinalization(t *testing.T) {
	cause := errors.New("private provider failure")
	unknownResult := executor.PublicationExecutionResult{ExitCode: provider.UnknownProviderExitCode, Completion: backupasset.CompletionOutcomeUnknown}
	cases := []struct {
		name     string
		err      error
		result   executor.PublicationExecutionResult
		reject   bool
		fail     backupasset.PublicationFailureCode
		suppress bool
		quiesced *bool
	}{
		{"unattempted", provider.RcloneUnattemptedPublicationError(cause), executor.PublicationExecutionResult{ExitCode: -1}, true, "", false, nil},
		{"positive exit", &provider.RclonePublicationError{MutationAttempted: true, WritersQuiesced: true, PositiveExitCode: 7, Cause: cause}, executor.PublicationExecutionResult{ExitCode: 7, Completion: backupasset.CompletionKnownNonzero}, false, backupasset.FailureProviderNonzeroExit, false, nil},
		{"semantic failure", &provider.RclonePublicationError{MutationAttempted: true, WritersQuiesced: true, Cause: cause}, executor.PublicationExecutionResult{ExitCode: -1}, false, backupasset.FailureProviderCompletionUnproven, false, nil},
		{"confirmed canceled", &provider.RclonePublicationError{MutationAttempted: true, WritersQuiesced: true, Cause: context.Canceled}, executor.PublicationExecutionResult{ExitCode: -1}, false, backupasset.FailureProviderCompletionUnproven, false, nil},
		{"unknown transport", &provider.RclonePublicationError{MutationAttempted: true, Cause: cause}, unknownResult, false, "", true, new(bool)},
		{"commit readback lost", &provider.RclonePublicationError{MutationAttempted: true, CommitAttempted: true, WritersQuiesced: true, Cause: cause}, unknownResult, false, "", true, new(true)},
		{"unmarked", cause, unknownResult, false, "", true, nil},
		{"contradictory", &provider.RclonePublicationError{CommitAttempted: true, WritersQuiesced: true, Cause: cause}, unknownResult, false, "", true, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			attempt := publicationRcloneAttempt(7, 9)
			input := provider.RclonePublicationInput{PortableRequest: &provider.RclonePortablePublicationRequest{Attempt: *attempt.Rclone}}
			session := &publicationExecutionFake{mode: publication.ModeEvidence, attempt: attempt, rcloneInput: &input}
			worker := &evidenceExecutorFake{result: test.result, err: test.err}
			manager := &Manager{executorFactory: executorFactoryFake{executor: worker}, publicationCoordinator: &publicationCoordinatorFake{execution: session}}
			result := manager.executeProvider(context.Background(), model.Task{ID: 7, ExecutorType: "rclone", RsyncSource: "/source"}, 9, "manual", "", nil, nil, nil)
			if !errors.Is(result.Err, test.err) || result.SuppressRetry != test.suppress || session.abandonCalls != 0 {
				t.Fatalf("result=%+v abandons=%d", result, session.abandonCalls)
			}
			if test.reject {
				if session.rejectCode != backupasset.FailurePublicationPreconditionMissing || session.failCode != "" || session.deferCall != nil {
					t.Fatal("unattempted publication was not rejected")
				}
			} else if test.fail != "" {
				if session.failCode != test.fail || session.rejectCode != "" || session.deferCall != nil {
					t.Fatalf("failed publication classification=%q", session.failCode)
				}
				if test.fail == backupasset.FailureProviderCompletionUnproven && result.ExitCode != -1 {
					t.Fatal("semantic failure fabricated an exit status")
				}
			} else {
				if session.deferCall == nil || session.deferCall.Completion != backupasset.CompletionOutcomeUnknown || result.ExitCode != -1 {
					t.Fatal("unknown publication was not deferred")
				}
				if (session.deferCall.RcloneWriterQuiesced == nil) != (test.quiesced == nil) || test.quiesced != nil && *session.deferCall.RcloneWriterQuiesced != *test.quiesced {
					t.Fatal("writer fact was not retained independently")
				}
			}
		})
	}
}
