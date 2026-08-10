package recovery

import "testing"

func TestStatePlanLifecycleGuardsExecutedSupersede(t *testing.T) {
	if !PlanStateDraft.CanTransitionTo(PlanStatePreflightReady, PlanTransitionGuard{}) {
		t.Fatal("draft must move to preflight_ready")
	}
	if !PlanStatePreflightReady.CanTransitionTo(PlanStateAuthorized, PlanTransitionGuard{}) {
		t.Fatal("preflight_ready must move to authorized")
	}
	if !PlanStateAuthorized.CanTransitionTo(PlanStateExecuted, PlanTransitionGuard{}) {
		t.Fatal("authorized must move to executed")
	}

	guard := PlanTransitionGuard{
		HasDurableJob:        true,
		HasCurrentFence:      true,
		TargetAtBaseRevision: true,
	}
	if !PlanStateExecuted.CanTransitionTo(PlanStateSuperseded, guard) {
		t.Fatal("pre-write executed plan must be supersedable by its current fence owner")
	}

	for _, invalid := range []struct {
		name  string
		guard PlanTransitionGuard
	}{
		{name: "missing job", guard: PlanTransitionGuard{HasCurrentFence: true, TargetAtBaseRevision: true}},
		{name: "missing current fence", guard: PlanTransitionGuard{HasDurableJob: true, TargetAtBaseRevision: true}},
		{name: "mutation armed", guard: PlanTransitionGuard{HasDurableJob: true, HasCurrentFence: true, MutationArmed: true, TargetAtBaseRevision: true}},
		{name: "checkpoint exists", guard: PlanTransitionGuard{HasDurableJob: true, HasCurrentFence: true, HasCheckpoint: true, TargetAtBaseRevision: true}},
		{name: "target drifted after write boundary", guard: PlanTransitionGuard{HasDurableJob: true, HasCurrentFence: true}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if PlanStateExecuted.CanTransitionTo(PlanStateSuperseded, invalid.guard) {
				t.Fatal("executed -> superseded bypassed its pre-write guard")
			}
		})
	}

	if PlanStateDraft.CanTransitionTo(PlanStateExecuted, PlanTransitionGuard{}) {
		t.Fatal("draft must not skip preflight and authorization")
	}
	if PlanStateSuperseded.CanTransitionTo(PlanStateExecuted, PlanTransitionGuard{}) {
		t.Fatal("superseded plan must remain terminal")
	}
}

func TestStateJobOutcomeLifecycleIsClosed(t *testing.T) {
	tests := []struct {
		from JobState
		to   JobState
		want bool
	}{
		{JobStateQueued, JobStateRunning, true},
		{JobStateQueued, JobStateCancelRequested, true},
		{JobStateRunning, JobStateVerifying, true},
		{JobStateRunning, JobStateCancelRequested, true},
		{JobStateVerifying, JobStateSucceeded, true},
		{JobStateVerifying, JobStateDegraded, true},
		{JobStateVerifying, JobStateFailed, true},
		{JobStateVerifying, JobStateNeedsAttention, true},
		{JobStateCancelRequested, JobStateCanceled, true},
		{JobStateCancelRequested, JobStateNeedsAttention, true},
		{JobStateSucceeded, JobStateRunning, false},
		{JobStateQueued, JobStateSucceeded, false},
		{JobState("unknown"), JobStateRunning, false},
	}

	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Fatalf("%q -> %q = %t, want %t", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestStateTerminalJobRejectsActiveAttemptCrossStatePairs(t *testing.T) {
	terminalJobs := []JobState{
		JobStateSucceeded,
		JobStateDegraded,
		JobStateFailed,
		JobStateNeedsAttention,
		JobStateCanceled,
	}
	activeAttempts := []AttemptState{AttemptStateClaimed, AttemptStateRunning}

	for _, jobState := range terminalJobs {
		for _, attemptState := range activeAttempts {
			if jobState.AllowsAttemptState(attemptState) {
				t.Fatalf("terminal job %q accepted active attempt %q", jobState, attemptState)
			}
		}
		if !jobState.AllowsAttemptState(AttemptStateCompleted) {
			t.Fatalf("terminal job %q rejected a closed attempt", jobState)
		}
	}

	if !JobStateRunning.AllowsAttemptState(AttemptStateRunning) {
		t.Fatal("non-terminal job rejected its active attempt")
	}
	if JobState("unknown").AllowsAttemptState(AttemptStateRunning) {
		t.Fatal("invalid job state accepted an attempt")
	}
	if JobStateRunning.AllowsAttemptState(AttemptState("unknown")) {
		t.Fatal("job state accepted an invalid attempt state")
	}
}

func TestStateResultSetRetryAndTakeoverRequireFreshFence(t *testing.T) {
	if !ResultSetStateReady.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{FreshCleanupFence: true}) {
		t.Fatal("ready result set must begin fenced revocation")
	}
	if !ResultSetStateRevoking.CanTransitionTo(ResultSetStateCleanupFailed, ResultSetTransitionGuard{CurrentOwner: true}) {
		t.Fatal("current revoking owner must record cleanup failure")
	}
	if !ResultSetStateCleanupFailed.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{FreshCleanupFence: true}) {
		t.Fatal("cleanup_failed must retry with a fresh cleanup fence")
	}
	if !ResultSetStateRevoking.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{
		CleanupLeaseExpired: true,
		FreshCleanupFence:   true,
	}) {
		t.Fatal("expired revoking owner must support same-state fenced takeover")
	}
	if ResultSetStateRevoking.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{FreshCleanupFence: true}) {
		t.Fatal("same-state revoking transition accepted before owner expiry")
	}
	if ResultSetStateCleanupFailed.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{}) {
		t.Fatal("cleanup retry accepted without a fresh cleanup fence")
	}
	if ResultSetStateCleaned.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{FreshCleanupFence: true}) {
		t.Fatal("cleaned result set must remain terminal")
	}
}

func TestStateJobAndResultSetTransitionMatricesRejectTerminalBypasses(t *testing.T) {
	jobCases := []struct {
		from JobState
		to   JobState
		want bool
	}{
		{JobStateQueued, JobStateRunning, true},
		{JobStateQueued, JobStateCancelRequested, true},
		{JobStateQueued, JobStateSucceeded, false},
		{JobStateRunning, JobStateVerifying, true},
		{JobStateRunning, JobStateFailed, true},
		{JobStateRunning, JobStateNeedsAttention, true},
		{JobStateRunning, JobStateSucceeded, false},
		{JobStateVerifying, JobStateSucceeded, true},
		{JobStateVerifying, JobStateDegraded, true},
		{JobStateVerifying, JobStateFailed, true},
		{JobStateVerifying, JobStateNeedsAttention, true},
		{JobStateCancelRequested, JobStateCanceled, true},
		{JobStateCancelRequested, JobStateNeedsAttention, true},
		{JobStateSucceeded, JobStateRunning, false},
		{JobStateDegraded, JobStateRunning, false},
		{JobStateFailed, JobStateRunning, false},
		{JobStateNeedsAttention, JobStateRunning, false},
		{JobStateCanceled, JobStateRunning, false},
	}
	for _, testCase := range jobCases {
		if got := testCase.from.CanTransitionTo(testCase.to); got != testCase.want {
			t.Fatalf("job %q -> %q = %t, want %t", testCase.from, testCase.to, got, testCase.want)
		}
	}

	if ResultSetStateReady.CanTransitionTo(ResultSetStateCleaned, ResultSetTransitionGuard{CurrentOwner: true, FreshCleanupFence: true}) {
		t.Fatal("ready result set must not skip revoking")
	}
	if !ResultSetStateReady.CanTransitionTo(ResultSetStateRevoking, ResultSetTransitionGuard{FreshCleanupFence: true}) {
		t.Fatal("ready result set must enter revoking with a fresh fence")
	}
	if !ResultSetStateRevoking.CanTransitionTo(ResultSetStateCleaned, ResultSetTransitionGuard{CurrentOwner: true}) {
		t.Fatal("current revoking owner must be able to tombstone a cleaned result set")
	}
}

func TestStateWorkspaceAndCleanupPhasesAreClosed(t *testing.T) {
	workspaceTransitions := []struct {
		from WorkspacePhase
		to   WorkspacePhase
		want bool
	}{
		{WorkspacePhaseNone, WorkspacePhaseReserved, true},
		{WorkspacePhaseReserved, WorkspacePhaseMarkerCreated, true},
		{WorkspacePhaseMarkerCreated, WorkspacePhaseWriting, true},
		{WorkspacePhaseWriting, WorkspacePhaseSealed, true},
		{WorkspacePhaseWriting, WorkspacePhaseCleanupDue, true},
		{WorkspacePhaseSealed, WorkspacePhasePublished, true},
		{WorkspacePhaseSealed, WorkspacePhaseCleanupDue, true},
		{WorkspacePhaseCleanupDue, WorkspacePhaseCleaned, true},
		{WorkspacePhaseNone, WorkspacePhaseWriting, false},
		{WorkspacePhasePublished, WorkspacePhaseCleanupDue, false},
	}
	for _, tt := range workspaceTransitions {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Fatalf("workspace %q -> %q = %t, want %t", tt.from, tt.to, got, tt.want)
		}
	}

	cleanupTransitions := []struct {
		from CleanupPhase
		to   CleanupPhase
		want bool
	}{
		{CleanupPhaseClaimed, CleanupPhaseRevoked, true},
		{CleanupPhaseRevoked, CleanupPhaseDrained, true},
		{CleanupPhaseDrained, CleanupPhaseValidated, true},
		{CleanupPhaseValidated, CleanupPhaseDeleteStarted, true},
		{CleanupPhaseDeleteStarted, CleanupPhaseDeleted, true},
		{CleanupPhaseDeleted, CleanupPhaseTombstoned, true},
		{CleanupPhaseClaimed, CleanupPhaseDeleted, false},
		{CleanupPhaseTombstoned, CleanupPhaseClaimed, false},
		{CleanupPhase("unknown"), CleanupPhaseClaimed, false},
	}
	for _, tt := range cleanupTransitions {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Fatalf("cleanup %q -> %q = %t, want %t", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestStateAttemptTerminalIntegrityAndArmedClosures(t *testing.T) {
	currentIdentity := AttemptTransitionGuard{SameOwner: true, SameFence: true}
	armedIdentity := AttemptTransitionGuard{SameOwner: true, SameFence: true, MutationArmed: true}

	for _, terminal := range []AttemptState{
		AttemptStateCompleted,
		AttemptStateFailed,
		AttemptStateCanceled,
		AttemptStateLost,
	} {
		if !AttemptStateRunning.CanTransitionTo(terminal, armedIdentity) {
			t.Fatalf("armed running attempt must close as %q", terminal)
		}
	}
	if AttemptStateRunning.CanTransitionTo(AttemptStateSuperseded, armedIdentity) {
		t.Fatal("armed running attempt must not be superseded as if it were pre-write")
	}
	if !AttemptStateRunning.CanTransitionTo(AttemptStateSuperseded, currentIdentity) {
		t.Fatal("unarmed running attempt must support a current-owner supersede closure")
	}

	if AttemptStateLost.CanTransitionTo(AttemptStateRunning, currentIdentity) {
		t.Fatal("lost attempt resurrected to running")
	}
	if AttemptStateRunning.CanTransitionTo(AttemptStateFailed, AttemptTransitionGuard{SameFence: true}) {
		t.Fatal("attempt transition changed owner identity")
	}
	if AttemptStateRunning.CanTransitionTo(AttemptStateFailed, AttemptTransitionGuard{SameOwner: true}) {
		t.Fatal("attempt transition changed fence identity")
	}
}

func TestStateCheckpointAppendMatrixRequiresMonotonicCurrentFences(t *testing.T) {
	current := CheckpointAppendGuard{
		SameAttempt:      true,
		SameAttemptFence: true,
		SameNodeFence:    true,
		MutationArmed:    true,
		ExactMirror:      true,
		NextSequence:     0,
	}

	if !CanStartCheckpoint(CheckpointPhaseOperation, TargetModeInPlace, current) {
		t.Fatal("in-place work must start with an operation checkpoint")
	}
	if !CanStartCheckpoint(CheckpointPhaseWorkspaceReserved, TargetModeIsolated, current) {
		t.Fatal("isolated work must start with its durable workspace reservation")
	}
	if CanStartCheckpoint(CheckpointPhaseOperation, TargetModeIsolated, current) {
		t.Fatal("isolated work bypassed workspace reservation")
	}
	if CanStartCheckpoint(CheckpointPhaseWorkspaceReserved, TargetModeInPlace, current) {
		t.Fatal("in-place work admitted an isolated workspace")
	}

	operation := CheckpointCursor{Sequence: 0, Phase: CheckpointPhaseOperation}
	for _, next := range []CheckpointPhase{
		CheckpointPhaseOperation,
		CheckpointPhaseVerification,
		CheckpointPhaseDeleteAuthorityRequired,
	} {
		guard := current
		guard.NextSequence = 1
		if !operation.CanAppend(next, guard) {
			t.Fatalf("operation checkpoint must admit %q under current fences", next)
		}
	}

	required := CheckpointCursor{Sequence: 1, Phase: CheckpointPhaseDeleteAuthorityRequired}
	guard := current
	guard.NextSequence = 2
	if !required.CanAppend(CheckpointPhaseDeleteAuthorityConsumed, guard) {
		t.Fatal("delete authority checkpoint must admit only its consumed checkpoint")
	}
	for _, invalid := range []CheckpointPhase{
		CheckpointPhaseOperation,
		CheckpointPhaseVerification,
		CheckpointPhaseDeleteAuthorityRequired,
	} {
		if required.CanAppend(invalid, guard) {
			t.Fatalf("delete authority required checkpoint bypassed consumption with %q", invalid)
		}
	}

	verification := CheckpointCursor{Sequence: 3, Phase: CheckpointPhaseVerification}
	guard.NextSequence = 4
	if verification.CanAppend(CheckpointPhaseOperation, guard) {
		t.Fatal("verification checkpoint must remain terminal")
	}

	for _, stale := range []CheckpointAppendGuard{
		{SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, MutationArmed: true, ExactMirror: true, NextSequence: 2},
		{SameAttemptFence: true, SameNodeFence: true, MutationArmed: true, ExactMirror: true, NextSequence: 1},
		{SameAttempt: true, SameNodeFence: true, MutationArmed: true, ExactMirror: true, NextSequence: 1},
		{SameAttempt: true, SameAttemptFence: true, MutationArmed: true, ExactMirror: true, NextSequence: 1},
		{SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, ExactMirror: true, NextSequence: 1},
	} {
		if operation.CanAppend(CheckpointPhaseOperation, stale) {
			t.Fatal("checkpoint append accepted a stale sequence, attempt, or fence")
		}
	}

	if (CheckpointCursor{Sequence: 0, Phase: CheckpointPhase("unknown")}).CanAppend(
		CheckpointPhaseOperation,
		CheckpointAppendGuard{SameAttempt: true, SameAttemptFence: true, SameNodeFence: true, MutationArmed: true, ExactMirror: true, NextSequence: 1},
	) {
		t.Fatal("unknown checkpoint phase admitted an append")
	}
}

func TestStateOperationUnresolvedProductsAreClosedAndTerminal(t *testing.T) {
	for _, category := range []UnresolvedOperationCategory{
		UnresolvedOperationRevisionDisagreement,
		UnresolvedOperationVerificationMismatch,
		UnresolvedOperationWriteResultInvalid,
		UnresolvedOperationObservationInvalid,
	} {
		if !category.Valid() {
			t.Fatalf("unresolved operation category %q is invalid", category)
		}
	}
	if UnresolvedOperationCategory("unknown").Valid() {
		t.Fatal("unknown unresolved operation category was accepted")
	}

	for _, outcome := range []SourceRevalidationOutcome{
		SourceRevalidationMatched,
		SourceRevalidationDrifted,
		SourceRevalidationFailed,
	} {
		if !outcome.Valid() {
			t.Fatalf("source revalidation outcome %q is invalid", outcome)
		}
	}
	if SourceRevalidationOutcome("unknown").Valid() {
		t.Fatal("unknown source revalidation outcome was accepted")
	}

	guard := CheckpointAppendGuard{
		SameAttempt: true, SameAttemptFence: true, SameNodeFence: true,
		MutationArmed: true, ExactMirror: true,
	}
	if !CheckpointPhaseOperationUnresolved.Valid() {
		t.Fatal("operation_unresolved checkpoint phase is invalid")
	}
	if !CanStartCheckpoint(CheckpointPhaseOperationUnresolved, TargetModeInPlace, guard) {
		t.Fatal("first in-place operation could not terminate unresolved")
	}

	for _, previous := range []CheckpointPhase{
		CheckpointPhaseWorkspaceReserved,
		CheckpointPhaseOperation,
		CheckpointPhaseDeleteAuthorityConsumed,
	} {
		guard.NextSequence = 1
		if !((CheckpointCursor{Sequence: 0, Phase: previous}).CanAppend(
			CheckpointPhaseOperationUnresolved, guard,
		)) {
			t.Fatalf("checkpoint %q could not terminate unresolved", previous)
		}
	}

	unresolved := CheckpointCursor{Sequence: 1, Phase: CheckpointPhaseOperationUnresolved}
	guard.NextSequence = 2
	for _, next := range []CheckpointPhase{
		CheckpointPhaseOperation,
		CheckpointPhaseOperationUnresolved,
		CheckpointPhaseVerification,
		CheckpointPhaseDeleteAuthorityRequired,
	} {
		if unresolved.CanAppend(next, guard) {
			t.Fatalf("terminal operation_unresolved checkpoint admitted %q", next)
		}
	}
}
