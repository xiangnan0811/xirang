package provider

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
)

// RsyncPointDeletionAccess is the sealed managed-tree binding required for
// exact point deletion. A raw path or legacy mutable target is rejected.
type RsyncPointDeletionAccess struct {
	ManagedRoot        string `json:"-"`
	MarkerKey          []byte `json:"-"`
	Attempt            RsyncTreeAttemptV1
	CommitMarkerDigest string
	SourceFingerprint  string
	Command            *RemoteCommandAccess `json:"-"`
}

var rsyncUnlinkTestHook func(name string)

type RsyncPointDeleter struct {
	now func() time.Time
}

func NewRsyncPointDeleter(now func() time.Time) (*RsyncPointDeleter, error) {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RsyncPointDeleter{now: now}, nil
}

func (deleter *RsyncPointDeleter) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}

func (deleter *RsyncPointDeleter) DeletePoint(ctx context.Context, request DeletePointRequest) (DeletePointResult, error) {
	if err := ctx.Err(); err != nil {
		return DeletePointResult{}, err
	}
	if err := request.requireSourceRevision(); err != nil {
		return DeletePointResult{}, err
	}
	access, err := rsyncDeletionAccess(request)
	if err != nil {
		return DeletePointResult{}, err
	}
	if request.ExpectedSourceRevision != access.SourceFingerprint {
		return DeletePointResult{}, ErrDeletePointIdentityConflict
	}
	tree, err := openRsyncManagedTree(access.ManagedRoot)
	if err != nil {
		return DeletePointResult{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := tree.VerifyRootIdentity(); err != nil {
		return DeletePointResult{}, err
	}
	exists, err := tree.finalComponentExists(access.Attempt.FinalComponent)
	if err != nil {
		return DeletePointResult{}, err
	}
	if !exists {
		if err := tree.VerifyRootIdentity(); err != nil {
			return DeletePointResult{}, err
		}
		return deleter.receipt(DeletePointAlreadyAbsent, request, access)
	}
	if err := verifyRsyncCommittedPointMarkers(tree, access); err != nil {
		return DeletePointResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DeletePointResult{}, err
	}
	if err := tree.DeleteCommittedPoint(ctx, access.Attempt.FinalComponent); err != nil {
		return DeletePointResult{}, err
	}
	exists, err = tree.finalComponentExists(access.Attempt.FinalComponent)
	if err != nil {
		return DeletePointResult{}, err
	}
	if exists {
		return DeletePointResult{}, fmt.Errorf("%w: committed Rsync point remained after delete", backupasset.ErrInvalidState)
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return DeletePointResult{}, err
	}
	return deleter.receipt(DeletePointDeleted, request, access)
}

func (deleter *RsyncPointDeleter) receipt(outcome DeletePointOutcome, request DeletePointRequest, access RsyncPointDeletionAccess) (DeletePointResult, error) {
	digest, err := deletionReceiptDigest(backupasset.ProviderRsync, outcome, request.OperationID, access.Attempt.FinalComponent+":"+access.CommitMarkerDigest)
	if err != nil {
		return DeletePointResult{}, err
	}
	return DeletePointResult{Outcome: outcome, ReceiptDigest: digest}, nil
}

func rsyncDeletionAccess(request DeletePointRequest) (RsyncPointDeletionAccess, error) {
	if request.Snapshot.Access.Provider != backupasset.ProviderRsync {
		return RsyncPointDeletionAccess{}, invalidDeletePointRequest("invalid Rsync deletion provider")
	}
	access, ok := request.Snapshot.Access.AdapterData.(RsyncPointDeletionAccess)
	if !ok || access.Attempt.Validate() != nil || !validRsyncTreeMarkerKey(access.MarkerKey) ||
		!validTaggedDigest(access.CommitMarkerDigest) || !validTaggedDigest(access.SourceFingerprint) {
		return RsyncPointDeletionAccess{}, invalidDeletePointRequest("invalid Rsync deletion access")
	}
	if _, err := normalizeRsyncManagedRoot(access.ManagedRoot); err != nil {
		return RsyncPointDeletionAccess{}, invalidDeletePointRequest("invalid Rsync managed root")
	}
	if absoluteOrPathShapedLocator(request.Point.Native) || request.Point.Native != access.Attempt.FinalComponent ||
		!validRsyncManagedTreeComponent(request.Point.Native) {
		return RsyncPointDeletionAccess{}, invalidDeletePointRequest("committed managed Rsync component required")
	}
	return access, nil
}

func verifyRsyncCommittedPointMarkers(tree *rsyncManagedTree, access RsyncPointDeletionAccess) error {
	storedAttempt, err := tree.readFinalMetadata(access.Attempt.FinalComponent, "attempt.json")
	if err != nil {
		return err
	}
	attempt, err := decodeRsyncTreeAttemptMarkerV1(storedAttempt, access.MarkerKey)
	if err != nil {
		return err
	}
	if attempt != access.Attempt {
		return ErrDeletePointIdentityConflict
	}
	storedCommit, err := tree.readFinalMetadata(access.Attempt.FinalComponent, "commit.json")
	if err != nil {
		return err
	}
	commit, err := decodeRsyncTreeCommitMarkerV1(storedCommit, access.MarkerKey)
	if err != nil {
		return err
	}
	if commit.CommitMarkerDigest != access.CommitMarkerDigest || commit.SourceFingerprint != access.SourceFingerprint ||
		commit.RecoveryPointID != access.Attempt.RecoveryPointID || commit.AttemptID != access.Attempt.AttemptID {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

var _ PointDeleter = (*RsyncPointDeleter)(nil)
