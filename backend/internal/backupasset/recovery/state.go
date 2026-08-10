package recovery

type PlanState string

const (
	PlanStateDraft          PlanState = "draft"
	PlanStatePreflightReady PlanState = "preflight_ready"
	PlanStateAuthorized     PlanState = "authorized"
	PlanStateExecuted       PlanState = "executed"
	PlanStateCanceled       PlanState = "canceled"
	PlanStateSuperseded     PlanState = "superseded"
	PlanStateExpired        PlanState = "expired"
)

type PlanTransitionGuard struct {
	HasDurableJob        bool
	HasCurrentFence      bool
	MutationArmed        bool
	HasCheckpoint        bool
	TargetAtBaseRevision bool
}

func (state PlanState) CanTransitionTo(next PlanState, guard PlanTransitionGuard) bool {
	if !state.Valid() || !next.Valid() {
		return false
	}

	switch state {
	case PlanStateDraft:
		return next == PlanStatePreflightReady || terminalPlanState(next)
	case PlanStatePreflightReady:
		return next == PlanStateAuthorized || terminalPlanState(next)
	case PlanStateAuthorized:
		return next == PlanStateExecuted || terminalPlanState(next)
	case PlanStateExecuted:
		return next == PlanStateSuperseded && guard.HasDurableJob && guard.HasCurrentFence &&
			!guard.MutationArmed && !guard.HasCheckpoint && guard.TargetAtBaseRevision
	default:
		return false
	}
}

func (state PlanState) Valid() bool {
	switch state {
	case PlanStateDraft, PlanStatePreflightReady, PlanStateAuthorized, PlanStateExecuted,
		PlanStateCanceled, PlanStateSuperseded, PlanStateExpired:
		return true
	default:
		return false
	}
}

func terminalPlanState(state PlanState) bool {
	return state == PlanStateCanceled || state == PlanStateSuperseded || state == PlanStateExpired
}

type JobState string

const (
	JobStateQueued          JobState = "queued"
	JobStateRunning         JobState = "running"
	JobStateVerifying       JobState = "verifying"
	JobStateSucceeded       JobState = "succeeded"
	JobStateDegraded        JobState = "degraded"
	JobStateNeedsAttention  JobState = "needs_attention"
	JobStateFailed          JobState = "failed"
	JobStateCancelRequested JobState = "cancel_requested"
	JobStateCanceled        JobState = "canceled"
)

func (state JobState) CanTransitionTo(next JobState) bool {
	if !state.Valid() || !next.Valid() {
		return false
	}

	switch state {
	case JobStateQueued:
		return next == JobStateRunning || next == JobStateCancelRequested
	case JobStateRunning:
		return next == JobStateVerifying || next == JobStateCancelRequested ||
			next == JobStateFailed || next == JobStateNeedsAttention
	case JobStateVerifying:
		return next == JobStateSucceeded || next == JobStateDegraded || next == JobStateFailed ||
			next == JobStateNeedsAttention || next == JobStateCancelRequested
	case JobStateCancelRequested:
		return next == JobStateCanceled || next == JobStateNeedsAttention
	default:
		return false
	}
}

func (state JobState) Valid() bool {
	switch state {
	case JobStateQueued, JobStateRunning, JobStateVerifying, JobStateSucceeded, JobStateDegraded,
		JobStateNeedsAttention, JobStateFailed, JobStateCancelRequested, JobStateCanceled:
		return true
	default:
		return false
	}
}

// AllowsAttemptState enforces the durable cross-row rule that a terminal job
// cannot retain or admit an active attempt.
func (state JobState) AllowsAttemptState(attempt AttemptState) bool {
	if !state.Valid() || !attempt.Valid() {
		return false
	}
	return !terminalJobState(state) || !activeAttemptState(attempt)
}

func terminalJobState(state JobState) bool {
	switch state {
	case JobStateSucceeded, JobStateDegraded, JobStateFailed, JobStateNeedsAttention, JobStateCanceled:
		return true
	default:
		return false
	}
}

type AttemptState string

const (
	AttemptStateClaimed    AttemptState = "claimed"
	AttemptStateRunning    AttemptState = "running"
	AttemptStateCompleted  AttemptState = "completed"
	AttemptStateFailed     AttemptState = "failed"
	AttemptStateCanceled   AttemptState = "canceled"
	AttemptStateSuperseded AttemptState = "superseded"
	AttemptStateLost       AttemptState = "lost"
)

type AttemptTransitionGuard struct {
	SameOwner     bool
	SameFence     bool
	MutationArmed bool
}

func (state AttemptState) CanTransitionTo(next AttemptState, guard AttemptTransitionGuard) bool {
	if !state.Valid() || !next.Valid() || !guard.SameOwner || !guard.SameFence {
		return false
	}

	switch state {
	case AttemptStateClaimed:
		return !guard.MutationArmed && (next == AttemptStateRunning || next == AttemptStateSuperseded || next == AttemptStateLost)
	case AttemptStateRunning:
		if next == AttemptStateSuperseded {
			return !guard.MutationArmed
		}
		return next == AttemptStateCompleted || next == AttemptStateFailed || next == AttemptStateCanceled || next == AttemptStateLost
	default:
		return false
	}
}

func (state AttemptState) Valid() bool {
	switch state {
	case AttemptStateClaimed, AttemptStateRunning, AttemptStateCompleted, AttemptStateFailed,
		AttemptStateCanceled, AttemptStateSuperseded, AttemptStateLost:
		return true
	default:
		return false
	}
}

func activeAttemptState(state AttemptState) bool {
	return state == AttemptStateClaimed || state == AttemptStateRunning
}

type CheckpointPhase string

const (
	CheckpointPhaseOperation               CheckpointPhase = "operation"
	CheckpointPhaseOperationUnresolved     CheckpointPhase = "operation_unresolved"
	CheckpointPhaseDeleteAuthorityRequired CheckpointPhase = "delete_authority_required"
	CheckpointPhaseDeleteAuthorityConsumed CheckpointPhase = "delete_authority_consumed"
	CheckpointPhaseVerification            CheckpointPhase = "verification"
	CheckpointPhaseWorkspaceReserved       CheckpointPhase = "workspace_reserved"
)

func (phase CheckpointPhase) Valid() bool {
	switch phase {
	case CheckpointPhaseOperation, CheckpointPhaseOperationUnresolved, CheckpointPhaseDeleteAuthorityRequired,
		CheckpointPhaseDeleteAuthorityConsumed, CheckpointPhaseVerification,
		CheckpointPhaseWorkspaceReserved:
		return true
	default:
		return false
	}
}

type CheckpointAppendGuard struct {
	SameAttempt      bool
	SameAttemptFence bool
	SameNodeFence    bool
	MutationArmed    bool
	ExactMirror      bool
	NextSequence     int
}

func (guard CheckpointAppendGuard) current() bool {
	return guard.SameAttempt && guard.SameAttemptFence && guard.SameNodeFence && guard.MutationArmed
}

func CanStartCheckpoint(next CheckpointPhase, targetMode TargetMode, guard CheckpointAppendGuard) bool {
	if !next.Valid() || !guard.current() || guard.NextSequence != 0 {
		return false
	}
	switch targetMode {
	case TargetModeIsolated:
		return next == CheckpointPhaseWorkspaceReserved
	case TargetModeInPlace:
		return next == CheckpointPhaseOperation || next == CheckpointPhaseOperationUnresolved
	default:
		return false
	}
}

type CheckpointCursor struct {
	Sequence int
	Phase    CheckpointPhase
}

func (cursor CheckpointCursor) CanAppend(next CheckpointPhase, guard CheckpointAppendGuard) bool {
	if cursor.Sequence < 0 || !cursor.Phase.Valid() || !next.Valid() || !guard.current() ||
		guard.NextSequence != cursor.Sequence+1 {
		return false
	}

	switch cursor.Phase {
	case CheckpointPhaseWorkspaceReserved:
		return next == CheckpointPhaseOperation || next == CheckpointPhaseOperationUnresolved
	case CheckpointPhaseOperation:
		return next == CheckpointPhaseOperation || next == CheckpointPhaseOperationUnresolved ||
			next == CheckpointPhaseVerification ||
			(next == CheckpointPhaseDeleteAuthorityRequired && guard.ExactMirror)
	case CheckpointPhaseDeleteAuthorityRequired:
		return next == CheckpointPhaseDeleteAuthorityConsumed && guard.ExactMirror
	case CheckpointPhaseDeleteAuthorityConsumed:
		return guard.ExactMirror && (next == CheckpointPhaseOperation ||
			next == CheckpointPhaseOperationUnresolved || next == CheckpointPhaseVerification)
	default:
		return false
	}
}

type UnresolvedOperationCategory string

const (
	UnresolvedOperationRevisionDisagreement UnresolvedOperationCategory = "revision_disagreement"
	UnresolvedOperationVerificationMismatch UnresolvedOperationCategory = "verification_mismatch"
	UnresolvedOperationWriteResultInvalid   UnresolvedOperationCategory = "write_result_invalid"
	UnresolvedOperationObservationInvalid   UnresolvedOperationCategory = "observation_invalid"
)

func (category UnresolvedOperationCategory) Valid() bool {
	switch category {
	case UnresolvedOperationRevisionDisagreement, UnresolvedOperationVerificationMismatch,
		UnresolvedOperationWriteResultInvalid, UnresolvedOperationObservationInvalid:
		return true
	default:
		return false
	}
}

type SourceRevalidationOutcome string

const (
	SourceRevalidationMatched SourceRevalidationOutcome = "matched"
	SourceRevalidationDrifted SourceRevalidationOutcome = "drifted"
	SourceRevalidationFailed  SourceRevalidationOutcome = "failed"
)

func (outcome SourceRevalidationOutcome) Valid() bool {
	switch outcome {
	case SourceRevalidationMatched, SourceRevalidationDrifted, SourceRevalidationFailed:
		return true
	default:
		return false
	}
}

type ResultSetState string

const (
	ResultSetStateReady         ResultSetState = "ready"
	ResultSetStateRevoking      ResultSetState = "revoking"
	ResultSetStateCleaned       ResultSetState = "cleaned"
	ResultSetStateCleanupFailed ResultSetState = "cleanup_failed"
)

type ResultSetTransitionGuard struct {
	CurrentOwner        bool
	CleanupLeaseExpired bool
	FreshCleanupFence   bool
}

func (state ResultSetState) CanTransitionTo(next ResultSetState, guard ResultSetTransitionGuard) bool {
	if !state.Valid() || !next.Valid() {
		return false
	}

	switch state {
	case ResultSetStateReady:
		return next == ResultSetStateRevoking && guard.FreshCleanupFence
	case ResultSetStateRevoking:
		if next == ResultSetStateRevoking {
			return guard.CleanupLeaseExpired && guard.FreshCleanupFence
		}
		return guard.CurrentOwner && (next == ResultSetStateCleaned || next == ResultSetStateCleanupFailed)
	case ResultSetStateCleanupFailed:
		return next == ResultSetStateRevoking && guard.FreshCleanupFence
	default:
		return false
	}
}

func (state ResultSetState) Valid() bool {
	switch state {
	case ResultSetStateReady, ResultSetStateRevoking, ResultSetStateCleaned, ResultSetStateCleanupFailed:
		return true
	default:
		return false
	}
}

type WorkspacePhase string

const (
	WorkspacePhaseNone          WorkspacePhase = "none"
	WorkspacePhaseReserved      WorkspacePhase = "reserved"
	WorkspacePhaseMarkerCreated WorkspacePhase = "marker_created"
	WorkspacePhaseWriting       WorkspacePhase = "writing"
	WorkspacePhaseSealed        WorkspacePhase = "sealed"
	WorkspacePhasePublished     WorkspacePhase = "published"
	WorkspacePhaseCleanupDue    WorkspacePhase = "cleanup_due"
	WorkspacePhaseCleaned       WorkspacePhase = "workspace_cleaned"
)

func (phase WorkspacePhase) CanTransitionTo(next WorkspacePhase) bool {
	if !phase.Valid() || !next.Valid() {
		return false
	}

	switch phase {
	case WorkspacePhaseNone:
		return next == WorkspacePhaseReserved
	case WorkspacePhaseReserved:
		return next == WorkspacePhaseMarkerCreated || next == WorkspacePhaseCleanupDue
	case WorkspacePhaseMarkerCreated:
		return next == WorkspacePhaseWriting || next == WorkspacePhaseCleanupDue
	case WorkspacePhaseWriting:
		return next == WorkspacePhaseSealed || next == WorkspacePhaseCleanupDue
	case WorkspacePhaseSealed:
		return next == WorkspacePhasePublished || next == WorkspacePhaseCleanupDue
	case WorkspacePhaseCleanupDue:
		return next == WorkspacePhaseCleaned
	default:
		return false
	}
}

func (phase WorkspacePhase) Valid() bool {
	switch phase {
	case WorkspacePhaseNone, WorkspacePhaseReserved, WorkspacePhaseMarkerCreated, WorkspacePhaseWriting,
		WorkspacePhaseSealed, WorkspacePhasePublished, WorkspacePhaseCleanupDue, WorkspacePhaseCleaned:
		return true
	default:
		return false
	}
}

type CleanupPhase string

const (
	CleanupPhaseClaimed       CleanupPhase = "claimed"
	CleanupPhaseRevoked       CleanupPhase = "revoked"
	CleanupPhaseDrained       CleanupPhase = "drained"
	CleanupPhaseValidated     CleanupPhase = "validated"
	CleanupPhaseDeleteStarted CleanupPhase = "delete_started"
	CleanupPhaseDeleted       CleanupPhase = "deleted"
	CleanupPhaseTombstoned    CleanupPhase = "tombstoned"
)

func (phase CleanupPhase) CanTransitionTo(next CleanupPhase) bool {
	if !phase.Valid() || !next.Valid() {
		return false
	}

	switch phase {
	case CleanupPhaseClaimed:
		return next == CleanupPhaseRevoked
	case CleanupPhaseRevoked:
		return next == CleanupPhaseDrained
	case CleanupPhaseDrained:
		return next == CleanupPhaseValidated
	case CleanupPhaseValidated:
		return next == CleanupPhaseDeleteStarted
	case CleanupPhaseDeleteStarted:
		return next == CleanupPhaseDeleted
	case CleanupPhaseDeleted:
		return next == CleanupPhaseTombstoned
	default:
		return false
	}
}

func (phase CleanupPhase) Valid() bool {
	switch phase {
	case CleanupPhaseClaimed, CleanupPhaseRevoked, CleanupPhaseDrained, CleanupPhaseValidated,
		CleanupPhaseDeleteStarted, CleanupPhaseDeleted, CleanupPhaseTombstoned:
		return true
	default:
		return false
	}
}
