package provider

import (
	"context"

	"xirang/backend/internal/backupasset"
)

// RsyncRestoreRunner is the provider-local execution seam for an exact Rsync
// restore. It intentionally accepts no shell, argv, remote path, credential,
// or unbounded locator. The source locator remains private to this package and
// the target is represented only by its frozen binding.
type RsyncRestoreRunner interface {
	Preflight(context.Context, RsyncRestorePreflightCall) (RsyncRestoreRunnerEvidence, error)
	Execute(context.Context, RsyncRestoreExecuteCall) (RsyncRestoreRunnerResult, error)
	Verify(context.Context, RsyncRestoreVerifyCall) (RsyncRestoreRunnerEvidence, error)
	Reconcile(context.Context, RsyncRestoreReconcileCall) (RsyncRestoreRunnerEvidence, error)
}

// RsyncBoundRemoteTarget is an opaque target capability: it carries the
// frozen node/root/revision binding but never the remote root or path locator.
type RsyncBoundRemoteTarget struct {
	NodeID              uint
	RootID              string
	TargetBindingDigest string
	TargetPathDigest    string
	RootRevision        string
	TargetRevision      string
}

// RsyncRestoreIntent is the typed, frozen local-to-remote operation shared by
// all Rsync restore phases. Entries are copied before a runner observes them.
type RsyncRestoreIntent struct {
	// Source is a pinned declared-entry capability. It deliberately exposes no
	// source/root pathname, locator, or transport information to the runner.
	Source         RsyncRestoreSource
	TargetWriter   RsyncTargetWriter
	Target         RsyncBoundRemoteTarget
	Entries        []RestoreEntry
	ManifestDigest string
	Limits         RestoreLimits
	ConflictPolicy RestoreConflictPolicy
	Fence          RestoreFence
	Checkpoint     RestoreCheckpoint
}

type RsyncRestorePreflightCall struct {
	RsyncRestoreIntent
	Permit TargetPreflightPermit
}

type RsyncRestoreExecuteCall struct {
	RsyncRestoreIntent
	Permit   TargetMutationPermit
	Progress RestoreProgress
}

type RsyncRestoreVerifyCall struct {
	RsyncRestoreIntent
	Permit TargetVerifyPermit
}

type RsyncRestoreReconcileCall struct {
	RsyncRestoreIntent
	Permit TargetReconcilePermit
}

// RsyncRestoreRunnerEvidence contains only already-sanitized process evidence
// and independently verified target observation/checkpoint facts. Raw
// stdout/stderr stays confined to RestoreProcessEvidenceInput while it is
// converted by the runner. Read-only phases must echo the current bound target
// facts exactly; adapters never invent them from a request.
type RsyncRestoreRunnerEvidence struct {
	TargetBindingDigest string
	TargetRevision      string
	Checkpoint          RestoreCheckpoint
	Evidence            []RestoreEvidence
}

type RsyncRestoreRunnerResult = RsyncRestoreRunnerEvidence

// NewRsyncRestoreIntent is the Provider-owned typed runner input constructor.
// Repository supplies the pinned source only after resolving the scalar ref;
// this package never constructs an Rsync source from a locator or raw path.
func NewRsyncRestoreIntent(request RestoreRequest, source RsyncRestoreSource, targetWriter RsyncTargetWriter) (RsyncRestoreIntent, error) {
	if request.Provider != backupasset.ProviderRsync || request.ValidateIntent() != nil || request.Rsync == nil || source == nil || targetWriter == nil {
		return RsyncRestoreIntent{}, invalidRestoreRequest("invalid Rsync restore intent")
	}
	return RsyncRestoreIntent{
		Source:       source,
		TargetWriter: targetWriter,
		Target: RsyncBoundRemoteTarget{
			NodeID:              request.Target.NodeID,
			RootID:              request.Target.RootID,
			TargetBindingDigest: request.Target.BindingDigest,
			TargetPathDigest:    request.Target.TargetPathDigest,
			RootRevision:        request.Target.RootRevision,
			TargetRevision:      request.Target.TargetRevision,
		},
		Entries:        append([]RestoreEntry(nil), request.Entries...),
		ManifestDigest: request.Rsync.ManifestDigest,
		Limits:         request.Limits,
		ConflictPolicy: request.ConflictPolicy,
		Fence:          request.Fence,
		Checkpoint:     request.Checkpoint,
	}, nil
}

func ValidateRsyncRestoreRunnerObservation(request RestoreRequest, evidence RsyncRestoreRunnerEvidence) error {
	if evidence.TargetBindingDigest != request.Target.BindingDigest || evidence.TargetRevision != request.Target.TargetRevision ||
		evidence.Checkpoint != request.Checkpoint {
		return invalidRestoreRequest("invalid Rsync target observation")
	}
	return nil
}
