package recovery

import (
	"context"
	"path"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// targetAdoptionVerifyProof binds a read-only verification to the locked
// handoff. It carries derived artifacts, never the key used to derive them.
type targetAdoptionVerifyProof struct {
	durableDigest        string
	jobItemID            string
	operationDigest      string
	object               TargetObjectRef
	expectationKind      TargetPresenceKind
	expectedDigest       string
	expectedBytes        int64
	expectedPriorBytes   int64
	overwriteArtifacts   recoveryOverwriteArtifactBinding
	consumedCheckpointID string
	consumedGrantID      string
	consumedGrantDigest  string
	deleteArtifacts      recoveryDeleteArtifactBinding
}

type recoveryConsumedDeleteArtifactBinding struct {
	checkpointID string
	grantID      string
	grantDigest  string
	artifacts    recoveryDeleteArtifactBinding
}

// recoveryDeleteArtifactInputForAuthority is shared by mutation issuance and
// adoption, so both name the historical consumed grant's exact artifacts.
func recoveryDeleteArtifactInputForAuthority(
	authority targetDeletePermitProof,
	keyVersion int,
) recoveryDeleteArtifactBindingInput {
	binding := authority.sessionBinding
	return recoveryDeleteArtifactBindingInput{
		keyVersion: keyVersion, planID: binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
		jobID: authority.jobID, jobItemID: authority.jobItemID, operationDigest: authority.operationDigest,
		consumedCheckpointID: authority.consumedCheckpointID, consumedGrantID: authority.consumedGrantID,
		consumedGrantDigest: authority.consumedGrantDigest, targetMode: authority.targetMode,
		nodeID: binding.NodeID, rootID: binding.RootID, rootLocatorDigest: binding.RootLocatorDigest,
		rootRevision: binding.RootRevision, object: authority.object,
		expectedPrior: authority.expectedPrior, expectedPriorBytes: authority.expectedPriorBytes,
	}
}

func newRecoveryConsumedDeleteArtifactBinding(
	material backupasset.DomainKeyMaterial,
	binding recoveryTargetSessionBinding,
	jobID string,
	item model.BackupAssetRecoveryJobItem,
	operationDigest string,
	object TargetObjectRef,
	consumed model.BackupAssetRecoveryCheckpoint,
) (recoveryConsumedDeleteArtifactBinding, error) {
	authority := targetDeletePermitProof{
		sessionBinding: binding, jobID: jobID, jobItemID: item.ID, operationDigest: operationDigest,
		consumedCheckpointID: consumed.ID, consumedGrantID: consumed.DeleteGrantID,
		consumedGrantDigest: consumed.DeleteGrantBindingDigest, targetMode: TargetModeInPlace,
		object: object, expectedPrior: ExpectedTargetIdentity{
			Kind: ExpectedTargetIdentityKind(item.ExpectedPriorKind), Digest: item.ExpectedPriorDigest,
		}, expectedPriorBytes: item.ExpectedPriorBytes,
	}
	artifacts, err := deriveRecoveryDeleteArtifactBinding(
		material, recoveryDeleteArtifactInputForAuthority(authority, item.TargetLocatorKeyVersion),
	)
	if err != nil {
		return recoveryConsumedDeleteArtifactBinding{}, err
	}
	return recoveryConsumedDeleteArtifactBinding{
		checkpointID: authority.consumedCheckpointID, grantID: authority.consumedGrantID,
		grantDigest: authority.consumedGrantDigest, artifacts: artifacts,
	}, nil
}

func targetAdoptionVerifyProofDigest(proof *targetAdoptionVerifyProof) string {
	if proof == nil {
		return ""
	}
	overwrite, deleted := proof.overwriteArtifacts, proof.deleteArtifacts
	return framedDigest(
		"xirang/recovery/target-adoption-verify-proof/v1",
		proof.durableDigest, proof.jobItemID, proof.operationDigest,
		proof.object.RootID, proof.object.RootLocatorDigest, proof.object.TargetPathDigest,
		proof.object.PrivateRelativeLocator, string(proof.expectationKind), proof.expectedDigest,
		strconv.FormatInt(proof.expectedBytes, 10), strconv.FormatInt(proof.expectedPriorBytes, 10),
		strconv.Itoa(overwrite.keyVersion), overwrite.bindingDigest, overwrite.token,
		overwrite.intentComponent, overwrite.priorComponent, overwrite.postComponent,
		overwrite.publishedComponent, overwrite.intentDocument, overwrite.publishedDocument,
		proof.consumedCheckpointID, proof.consumedGrantID, proof.consumedGrantDigest,
		strconv.Itoa(deleted.keyVersion), deleted.bindingDigest, deleted.token,
		deleted.intentComponent, deleted.capturedComponent, deleted.verifiedComponent,
		deleted.intentDocument, deleted.verifiedDocument,
	)
}

func validTargetAdoptionVerifyProof(permit TargetObservationPermit, parent *targetVerifyPermitProof) bool {
	proof := parent.adoption
	if proof == nil || !validDigest(proof.durableDigest) || !validOpaqueID(proof.jobItemID) ||
		!validDigest(proof.operationDigest) || !proof.object.valid() || !permit.matchesObject(proof.object) ||
		validateRecoveryVerifyNamespace(proof.object.PrivateRelativeLocator, parent.jobID, parent.targetMode) != nil {
		return false
	}
	noOverwrite := proof.overwriteArtifacts == (recoveryOverwriteArtifactBinding{})
	noDelete := proof.consumedCheckpointID == "" && proof.consumedGrantID == "" &&
		proof.consumedGrantDigest == "" && proof.deleteArtifacts == (recoveryDeleteArtifactBinding{})
	switch parent.operation {
	case RecoveryOperationCreate, RecoveryOperationSkip:
		if !noOverwrite || !noDelete || proof.expectationKind != TargetPresencePresent ||
			!validDigest(proof.expectedDigest) || proof.expectedBytes < 0 {
			return false
		}
		if parent.operation == RecoveryOperationCreate {
			return proof.expectedPriorBytes == -1
		}
		return proof.expectedDigest == parent.expectedPrior.Digest && proof.expectedBytes == proof.expectedPriorBytes
	case RecoveryOperationOverwrite:
		if !noDelete || proof.expectationKind != TargetPresencePresent || !validDigest(proof.expectedDigest) ||
			proof.expectedBytes < 0 || proof.expectedPriorBytes < 0 {
			return false
		}
		return parent.targetMode == TargetModeInPlace && proof.overwriteArtifacts.valid()
	case RecoveryOperationDelete:
		return parent.targetMode == TargetModeInPlace && noOverwrite &&
			proof.expectationKind == TargetPresenceAbsent && proof.expectedDigest == "" && proof.expectedBytes == 0 &&
			proof.expectedPriorBytes == -1 && validOpaqueID(proof.consumedCheckpointID) &&
			validOpaqueID(proof.consumedGrantID) && validDigest(proof.consumedGrantDigest) && proof.deleteArtifacts.valid()
	default:
		return false
	}
}

func (proof targetAdoptionVerifyProof) matchesRequest(object TargetObjectRef, expectation TargetVerifyExpectation) bool {
	if proof.object != object || expectation.Validate() != nil || proof.expectationKind != expectation.Kind {
		return false
	}
	if expectation.Kind == TargetPresenceAbsent {
		return proof.expectedDigest == "" && proof.expectedBytes == 0
	}
	return expectation.Present.IdentityDigest == proof.expectedDigest && expectation.Present.Bytes == proof.expectedBytes
}

func newRecoveryTargetAdoptionVerifyPermit(
	handoff interruptedOperationHandoff,
	expiresAt time.Time,
	now time.Time,
) (TargetVerifyPermit, error) {
	permit, err := newRecoveryTargetVerifyPermit(handoff, expiresAt, now)
	if err != nil || !validDigest(handoff.durableDigest) || !validOpaqueID(handoff.item.ID) ||
		handoff.item.JobID != handoff.job.ID || handoff.item.PlanID != handoff.plan.ID ||
		!handoff.operation.validExpectedPostFacts() ||
		handoff.operation.TargetPathDigest != handoff.item.TargetPathDigest ||
		handoff.operation.SemanticTargetDigest != handoff.item.SemanticTargetDigest ||
		string(handoff.operation.DisplayClass) != handoff.item.DisplayClass ||
		handoff.operation.EstimatedBytes != handoff.item.EstimatedBytes ||
		handoff.operation.ExpectedPostIdentityDigest != handoff.item.ExpectedPostIdentityDigest ||
		handoff.operation.ExpectedPostBytes != handoff.item.ExpectedPostBytes ||
		handoff.operation.ExpectedPriorBytes != handoff.item.ExpectedPriorBytes ||
		(handoff.operation.Kind == RecoveryOperationDelete && !handoff.deleteAuthorityConsumed) {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	locator := handoff.operation.TargetRelativeLocator
	if TargetMode(handoff.job.TargetMode) == TargetModeIsolated {
		locator = handoff.job.EncryptedWorkspaceRelativeLocator + "/" + locator
	}
	if locator != handoff.object.PrivateRelativeLocator {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	expected, err := recoveryOperationVerifyExpectation(handoff.operation, handoff.item)
	if err != nil || handoff.expectation.Validate() != nil || expected.Kind != handoff.expectation.Kind ||
		(expected.Kind == TargetPresencePresent && *expected.Present != *handoff.expectation.Present) {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	adoption := &targetAdoptionVerifyProof{
		durableDigest: handoff.durableDigest, jobItemID: handoff.item.ID, operationDigest: handoff.operationDigest,
		object: handoff.object, expectationKind: expected.Kind, expectedPriorBytes: handoff.item.ExpectedPriorBytes,
		overwriteArtifacts:   handoff.overwriteArtifacts,
		consumedCheckpointID: handoff.consumedDeleteArtifacts.checkpointID,
		consumedGrantID:      handoff.consumedDeleteArtifacts.grantID,
		consumedGrantDigest:  handoff.consumedDeleteArtifacts.grantDigest,
		deleteArtifacts:      handoff.consumedDeleteArtifacts.artifacts,
	}
	if expected.Present != nil {
		adoption.expectedDigest, adoption.expectedBytes = expected.Present.IdentityDigest, expected.Present.Bytes
	}
	parent := permit.permit.proof
	parent.adoption = adoption
	parent.bindingDigest = targetVerifyPermitProofDigest(permit.permit, parent)
	if permit.ValidateObjectAt(now, handoff.object) != nil || !adoption.matchesRequest(handoff.object, handoff.expectation) {
		return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
	}
	return permit, nil
}

// recoveryAdoptionTuple is comparable so both the classification and every
// typed observed fact must remain unchanged across the independent final check.
type recoveryAdoptionTuple struct {
	overwriteState recoveryOverwriteTupleState
	overwrite      recoveryOverwriteTuple
	deleteState    recoveryDeleteTupleClassification
	deleted        recoveryDeleteTupleObservation
}

func (proof targetAdoptionVerifyProof) deleteAuthority(parent *targetVerifyPermitProof) targetDeletePermitProof {
	return targetDeletePermitProof{
		sessionBinding: parent.sessionBinding, jobID: parent.jobID, jobItemID: proof.jobItemID,
		operationDigest: proof.operationDigest, consumedCheckpointID: proof.consumedCheckpointID,
		consumedGrantID: proof.consumedGrantID, consumedGrantDigest: proof.consumedGrantDigest,
		targetMode: parent.targetMode, object: proof.object, expectedPrior: parent.expectedPrior,
		expectedPriorBytes: proof.expectedPriorBytes, artifacts: proof.deleteArtifacts,
	}
}

func (target *recoverySFTPTarget) adoptionMaterial(
	ctx context.Context,
	parent *targetVerifyPermitProof,
) (backupasset.DomainKeyMaterial, error) {
	proof := parent.adoption
	if proof == nil || (parent.operation != RecoveryOperationDelete &&
		(parent.operation != RecoveryOperationOverwrite || parent.targetMode != TargetModeInPlace)) {
		return backupasset.DomainKeyMaterial{}, nil
	}
	if target.marker == nil || target.marker.keys == nil {
		return backupasset.DomainKeyMaterial{}, ErrRecoveryTargetUnavailable
	}
	version := proof.overwriteArtifacts.keyVersion
	if parent.operation == RecoveryOperationDelete {
		version = proof.deleteArtifacts.keyVersion
	}
	material, err := target.marker.keys.ByVersion(ctx, backupasset.KeyDomainRecoveryCleanupOwnership, version)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, recoveryTargetUnavailableForContext(ctx)
	}
	if !validTargetLocatorKey(material, version) {
		clear(material.Key)
		return backupasset.DomainKeyMaterial{}, ErrRecoveryTargetUnavailable
	}
	var exact bool
	if parent.operation == RecoveryOperationDelete {
		authority := proof.deleteAuthority(parent)
		derived, deriveErr := deriveRecoveryDeleteArtifactBinding(material, recoveryDeleteArtifactInputForAuthority(authority, version))
		exact = deriveErr == nil && derived == proof.deleteArtifacts
	} else {
		binding := parent.sessionBinding
		derived, deriveErr := newRecoveryOverwriteArtifactBinding(material, recoveryOverwriteArtifactBindingInput{
			keyVersion: version, planID: binding.PlanID, planBindingDigest: binding.PlanBindingDigest,
			jobID: parent.jobID, jobItemID: proof.jobItemID, operationDigest: proof.operationDigest,
			targetMode: parent.targetMode, nodeID: binding.NodeID, rootID: binding.RootID,
			rootLocatorDigest: binding.RootLocatorDigest, rootRevision: binding.RootRevision,
			object: proof.object, expectedPrior: parent.expectedPrior, expectedPriorBytes: proof.expectedPriorBytes,
			expectedPostDigest: proof.expectedDigest, expectedPostBytes: proof.expectedBytes,
		})
		exact = deriveErr == nil && derived == proof.overwriteArtifacts
	}
	if !exact {
		clear(material.Key)
		return backupasset.DomainKeyMaterial{}, ErrInvalidTargetPermit
	}
	return material, nil
}

func observeRecoveryAdoptionTuple(
	client recoveryTargetSFTPClient,
	parent *targetVerifyPermitProof,
	material backupasset.DomainKeyMaterial,
) (recoveryAdoptionTuple, error) {
	proof := parent.adoption
	if proof == nil {
		return recoveryAdoptionTuple{}, nil
	}
	finalPath := path.Join(parent.sessionBinding.RootLocator, proof.object.PrivateRelativeLocator)
	switch {
	case parent.operation == RecoveryOperationOverwrite && parent.targetMode == TargetModeInPlace:
		authority := targetItemWriteAuthority{
			sessionBinding: parent.sessionBinding, jobID: parent.jobID, jobItemID: proof.jobItemID,
			operationDigest: proof.operationDigest, targetMode: parent.targetMode,
			operation: parent.operation, expectedPrior: parent.expectedPrior,
			expectedPriorBytes: proof.expectedPriorBytes, artifacts: proof.overwriteArtifacts,
		}
		request := TargetWriteAtomicRequest{Object: proof.object, ExpectedDigest: proof.expectedDigest, ExpectedBytes: proof.expectedBytes}
		paths, err := recoveryOverwriteArtifactPathsFor(finalPath, proof.overwriteArtifacts)
		if err != nil {
			return recoveryAdoptionTuple{}, err
		}
		state, tuple, err := classifyRecoveryOverwriteTuple(client, parent.sessionBinding, authority, request, paths)
		if err != nil {
			return recoveryAdoptionTuple{}, err
		}
		// Cleanup removes prior and post before intent. An acknowledged marker
		// with an absent intent and remaining payload is contradictory.
		if state != recoveryOverwriteTupleStatePublishedUnacknowledged &&
			(state != recoveryOverwriteTupleStateAcknowledged ||
				(tuple.intent.missing && (!tuple.prior.missing || !tuple.post.missing))) {
			return recoveryAdoptionTuple{}, ErrRecoveryTargetChanged
		}
		return recoveryAdoptionTuple{overwriteState: state, overwrite: tuple}, nil
	case parent.operation == RecoveryOperationDelete:
		authority := proof.deleteAuthority(parent)
		paths, err := recoveryDeleteArtifactPathsFor(finalPath, proof.deleteArtifacts)
		if err != nil {
			return recoveryAdoptionTuple{}, err
		}
		tuple, err := observeRecoveryDeleteTuple(client, authority, paths)
		if err != nil {
			return recoveryAdoptionTuple{}, err
		}
		classification := classifyRecoveryDeleteTuple(material, authority, tuple)
		if classification.state != recoveryDeleteTupleDeleted && classification.state != recoveryDeleteTupleClean {
			return recoveryAdoptionTuple{}, ErrRecoveryTargetChanged
		}
		return recoveryAdoptionTuple{deleteState: classification, deleted: tuple}, nil
	default:
		return recoveryAdoptionTuple{}, nil
	}
}
