package recovery

import "strconv"

// These phases are private authority inputs derived from locked job history.
// They never replace the immutable plan/preflight identity.
type recoveryAuthorityTargetPhase uint8

const (
	recoveryAuthorityTargetPreJob recoveryAuthorityTargetPhase = iota
	recoveryAuthorityTargetFreshFirstWrite
	recoveryAuthorityTargetCheckpointed
	recoveryAuthorityTargetCurrentPost
)

type recoveryAuthorityTargetIntent struct {
	phase  recoveryAuthorityTargetPhase
	itemID string
}

type recoveryAuthorityTargetExpectation struct {
	phase                  recoveryAuthorityTargetPhase
	jobID                  string
	jobTransitionRevision  uint64
	chainRevision          string
	historyDigest          string
	workerID               string
	attemptID              string
	attemptFence           uint64
	nodeLeaseID            string
	nodeFence              uint64
	sourceLeaseID          string
	sourceFenceDigest      string
	anchorItemID           string
	anchorOperationDigest  string
	anchorCheckpointID     string
	currentItemID          string
	currentOperationDigest string
	expectedDigest         string
	expectedBytes          int64
}

func (expectation recoveryAuthorityTargetExpectation) verifiesContent(mode TargetMode) bool {
	return mode == TargetModeInPlace && expectation.expectedDigest != "" &&
		(expectation.phase == recoveryAuthorityTargetCheckpointed || expectation.phase == recoveryAuthorityTargetCurrentPost)
}

func (expectation recoveryAuthorityTargetExpectation) valid(mode TargetMode) bool {
	if expectation.phase == recoveryAuthorityTargetPreJob {
		return expectation == (recoveryAuthorityTargetExpectation{})
	}
	if expectation.phase != recoveryAuthorityTargetFreshFirstWrite &&
		expectation.phase != recoveryAuthorityTargetCheckpointed && expectation.phase != recoveryAuthorityTargetCurrentPost {
		return false
	}
	if !validOpaqueID(expectation.jobID) || expectation.jobTransitionRevision == 0 ||
		!validOpaqueRevision(expectation.chainRevision) || !validDigest(expectation.historyDigest) ||
		!validRecoveryWorkerID(expectation.workerID) || !validOpaqueID(expectation.attemptID) || expectation.attemptFence == 0 ||
		!validOpaqueID(expectation.nodeLeaseID) || expectation.nodeFence == 0 ||
		!validOpaqueID(expectation.sourceLeaseID) || !validDigest(expectation.sourceFenceDigest) {
		return false
	}
	if (expectation.currentItemID == "") != (expectation.currentOperationDigest == "") ||
		(expectation.currentItemID != "" && (!validOpaqueID(expectation.currentItemID) || !validDigest(expectation.currentOperationDigest))) {
		return false
	}
	if expectation.phase == recoveryAuthorityTargetFreshFirstWrite && expectation.currentItemID != "" {
		return false
	}
	if mode == TargetModeIsolated {
		return expectation.phase != recoveryAuthorityTargetCurrentPost && expectation.anchorItemID == "" &&
			expectation.anchorOperationDigest == "" && expectation.anchorCheckpointID == "" &&
			expectation.expectedDigest == "" && expectation.expectedBytes == 0
	}
	if mode != TargetModeInPlace || !validOpaqueID(expectation.anchorItemID) || !validDigest(expectation.anchorOperationDigest) {
		return false
	}
	if expectation.phase == recoveryAuthorityTargetCurrentPost {
		return expectation.currentItemID == expectation.anchorItemID &&
			expectation.currentOperationDigest == expectation.anchorOperationDigest && expectation.anchorCheckpointID == "" &&
			validDigest(expectation.expectedDigest) && expectation.expectedBytes >= 0
	}
	if expectation.anchorCheckpointID == "" {
		return expectation.expectedDigest == "" && expectation.expectedBytes == 0
	}
	return expectation.phase == recoveryAuthorityTargetCheckpointed && validOpaqueID(expectation.anchorCheckpointID) &&
		validDigest(expectation.expectedDigest) && expectation.expectedBytes >= 0
}

func recoveryAuthorityTargetDigest(expectation recoveryAuthorityTargetExpectation) string {
	return framedDigest("xirang/recovery/authority-target-expectation/v1",
		strconv.Itoa(int(expectation.phase)), expectation.jobID, strconv.FormatUint(expectation.jobTransitionRevision, 10),
		expectation.chainRevision, expectation.historyDigest, expectation.workerID, expectation.attemptID,
		strconv.FormatUint(expectation.attemptFence, 10), expectation.nodeLeaseID, strconv.FormatUint(expectation.nodeFence, 10),
		expectation.sourceLeaseID, expectation.sourceFenceDigest, expectation.anchorItemID, expectation.anchorOperationDigest,
		expectation.anchorCheckpointID, expectation.currentItemID, expectation.currentOperationDigest,
		expectation.expectedDigest, strconv.FormatInt(expectation.expectedBytes, 10))
}

// Only callers that have already validated the real operation observation may
// request the pending anchor's post-state. New mutation permits use checkpointed.
func recoveryAuthorityVerifiedTargetIntent(handoff interruptedOperationHandoff) recoveryAuthorityTargetIntent {
	intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: handoff.item.ID}
	if TargetMode(handoff.plan.TargetMode) == TargetModeInPlace &&
		handoff.operation.TargetRelativeLocator == handoff.plan.EncryptedTargetRelativePath &&
		handoff.operation.TargetPathDigest == handoff.plan.PathDigest &&
		(handoff.operation.Kind == RecoveryOperationCreate || handoff.operation.Kind == RecoveryOperationOverwrite) {
		intent.phase = recoveryAuthorityTargetCurrentPost
	}
	return intent
}

func (recoveryAuthorityTargetExpectation) String() string {
	return "recoveryAuthorityTargetExpectation{redacted}"
}
func (recoveryAuthorityTargetExpectation) GoString() string {
	return "recoveryAuthorityTargetExpectation{redacted}"
}
func (recoveryAuthorityTargetIntent) String() string {
	return "recoveryAuthorityTargetIntent{redacted}"
}
func (recoveryAuthorityTargetIntent) GoString() string {
	return "recoveryAuthorityTargetIntent{redacted}"
}
