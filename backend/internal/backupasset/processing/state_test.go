package processing

import (
	"errors"
	"testing"
	"time"
)

func TestProcessingStateClosedSet(t *testing.T) {
	want := []ProcessingState{
		ProcessingQueued,
		ProcessingLeased,
		ProcessingFetching,
		ProcessingMaterializing,
		ProcessingProcessing,
		ProcessingUploading,
		ProcessingValidating,
		ProcessingRetryWait,
		ProcessingCancelRequested,
		ProcessingCanceled,
		ProcessingSucceeded,
		ProcessingFailed,
		ProcessingSuperseded,
		ProcessingExpired,
	}
	got := AllProcessingStates()
	if len(got) != len(want) {
		t.Fatalf("state count=%d, want %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] || !got[index].Valid() {
			t.Fatalf("state[%d]=%q, want %q valid", index, got[index], want[index])
		}
	}
	for _, invalid := range []ProcessingState{"", "fetching/materializing", "complete", "retrying"} {
		if invalid.Valid() {
			t.Fatalf("invalid state %q was accepted", invalid)
		}
	}
}

func TestProcessingStateStreamingAndMaterializingPathsAreIndependent(t *testing.T) {
	paths := [][]ProcessingState{
		{ProcessingLeased, ProcessingFetching, ProcessingProcessing},
		{ProcessingLeased, ProcessingFetching, ProcessingMaterializing, ProcessingProcessing},
	}
	for _, path := range paths {
		for index := 1; index < len(path); index++ {
			if err := ValidateStateTransition(path[index-1], path[index]); err != nil {
				t.Fatalf("transition %s -> %s: %v", path[index-1], path[index], err)
			}
		}
	}
	if err := ValidateStateTransition(ProcessingLeased, ProcessingMaterializing); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("leased -> materializing got %v, want ErrInvalidTransition", err)
	}
}

func TestProcessingStateRejectsTerminalExitAndShortcuts(t *testing.T) {
	testCases := [][2]ProcessingState{
		{ProcessingQueued, ProcessingCanceled},
		{ProcessingQueued, ProcessingSucceeded},
		{ProcessingFailed, ProcessingQueued},
		{ProcessingSucceeded, ProcessingQueued},
		{ProcessingCanceled, ProcessingFailed},
		{ProcessingExpired, ProcessingQueued},
	}
	for _, testCase := range testCases {
		if err := ValidateStateTransition(testCase[0], testCase[1]); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("transition %s -> %s got %v, want ErrInvalidTransition", testCase[0], testCase[1], err)
		}
	}
}

func TestProcessingTransitionRevisionAndOutcomeProducts(t *testing.T) {
	now := time.Date(2026, 7, 19, 4, 5, 6, 0, time.UTC)
	valid := []TransitionRequest{
		{From: ProcessingQueued, To: ProcessingLeased, CurrentRevision: 4, ExpectedRevision: 4},
		{From: ProcessingProcessing, To: ProcessingRetryWait, CurrentRevision: 8, ExpectedRevision: 8, ErrorCode: ProcessingErrorTimeout, RetryAt: ptrTime(now.Add(time.Minute))},
		{From: ProcessingUploading, To: ProcessingCancelRequested, CurrentRevision: 9, ExpectedRevision: 9, CancelReason: CancelReasonInterestWithdrawn},
		{From: ProcessingValidating, To: ProcessingSuperseded, CurrentRevision: 10, ExpectedRevision: 10, SupersedeReason: SupersedeReasonSourceChanged},
		{From: ProcessingFetching, To: ProcessingExpired, CurrentRevision: 11, ExpectedRevision: 11, ExpiryReason: ExpiryReasonDeadline},
		{From: ProcessingValidating, To: ProcessingFailed, CurrentRevision: 12, ExpectedRevision: 12, ErrorCode: ProcessingErrorInvalidOutput},
	}
	for _, request := range valid {
		next, err := ValidateTransition(request)
		if err != nil {
			t.Fatalf("ValidateTransition(%+v): %v", request, err)
		}
		if next != request.CurrentRevision+1 {
			t.Fatalf("next revision=%d, want %d", next, request.CurrentRevision+1)
		}
	}

	invalid := []TransitionRequest{
		{From: ProcessingQueued, To: ProcessingLeased, CurrentRevision: 4, ExpectedRevision: 3},
		{From: ProcessingProcessing, To: ProcessingRetryWait, CurrentRevision: 4, ExpectedRevision: 4, ErrorCode: ProcessingErrorUnsupportedFormat, RetryAt: ptrTime(now)},
		{From: ProcessingProcessing, To: ProcessingRetryWait, CurrentRevision: 4, ExpectedRevision: 4, ErrorCode: ProcessingErrorTimeout},
		{From: ProcessingProcessing, To: ProcessingFailed, CurrentRevision: 4, ExpectedRevision: 4, ErrorCode: ProcessingErrorTimeout},
		{From: ProcessingProcessing, To: ProcessingCancelRequested, CurrentRevision: 4, ExpectedRevision: 4},
		{From: ProcessingProcessing, To: ProcessingSuperseded, CurrentRevision: 4, ExpectedRevision: 4},
		{From: ProcessingProcessing, To: ProcessingExpired, CurrentRevision: 4, ExpectedRevision: 4},
	}
	for _, request := range invalid {
		if _, err := ValidateTransition(request); err == nil {
			t.Fatalf("invalid transition product was accepted: %+v", request)
		}
	}
}

func TestProcessingErrorCategoriesAreClosed(t *testing.T) {
	testCases := map[ProcessingErrorCode]ProcessingErrorCategory{
		ProcessingErrorUnsupportedFormat: PermanentError,
		ProcessingErrorSourceChanged:     PermanentError,
		ProcessingErrorWorkerUnavailable: TransientError,
		ProcessingErrorLeaseLost:         TransientError,
		ProcessingErrorInvalidOutput:     ContractSecurityError,
		ProcessingErrorNetworkViolation:  ContractSecurityError,
	}
	for code, want := range testCases {
		got, err := code.Category()
		if err != nil || got != want {
			t.Fatalf("Category(%q)=%q, %v; want %q", code, got, err, want)
		}
	}
	if _, err := ProcessingErrorCode("future_error").Category(); err == nil {
		t.Fatal("unknown error code was accepted")
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
