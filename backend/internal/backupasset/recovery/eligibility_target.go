package recovery

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/pkg/sftp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecoveryEligibilityTargetRootAuthorityDependencies are the two durable
// owners composed by Recovery. Registry owns the encrypted v2 root while
// Revisions owns purpose-exact current node and credential authority.
type RecoveryEligibilityTargetRootAuthorityDependencies struct {
	Registry  RecoveryTargetRootResolver
	Revisions RecoveryNodeRevisionSource
}

type recoveryEligibilityTargetRootAuthority struct {
	registry  RecoveryTargetRootResolver
	revisions RecoveryNodeRevisionSource
}

// NewRecoveryEligibilityTargetRootAuthority constructs the durable target
// root port. It performs no transaction and retains no private root product.
func NewRecoveryEligibilityTargetRootAuthority(
	dependencies RecoveryEligibilityTargetRootAuthorityDependencies,
) (RecoveryEligibilityTargetRootPort, error) {
	if dependencies.Registry == nil || dependencies.Revisions == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	return &recoveryEligibilityTargetRootAuthority{
		registry: dependencies.Registry, revisions: dependencies.Revisions,
	}, nil
}

func (authority *recoveryEligibilityTargetRootAuthority) CaptureRecoveryEligibilityTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	binding RecoveryAuthorityBinding,
) (RecoveryEligibilityTargetRootSnapshot, error) {
	if err := recoveryEligibilityTargetRootCallValid(authority, ctx, tx); err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, err
	}
	if err := lockRecoveryEligibilityTargetRootRowTx(
		ctx, tx, binding.TargetNodeID, binding.TargetRootID,
	); err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, err
	}
	root, err := authority.registry.ResolveRecoveryTargetRootTx(
		ctx, tx, binding.TargetNodeID, binding.TargetRootID,
	)
	if err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	revisions, err := authority.revisions.ResolveRecoveryNodeRevisionsTx(
		ctx, tx, binding.TargetNodeID, TargetPurposePreflight,
	)
	if err != nil {
		return RecoveryEligibilityTargetRootSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	snapshot := recoveryEligibilityTargetRootSnapshot(root, revisions)
	if !recoveryEligibilityTargetRootSnapshotValid(snapshot) ||
		!recoveryEligibilityTargetRootMatchesBinding(binding, snapshot) {
		return RecoveryEligibilityTargetRootSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return snapshot, nil
}

func (authority *recoveryEligibilityTargetRootAuthority) RevalidateRecoveryEligibilityTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	binding RecoveryAuthorityBinding,
	captured RecoveryEligibilityTargetRootSnapshot,
) error {
	if err := recoveryEligibilityTargetRootCallValid(authority, ctx, tx); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrRecoveryTargetChanged
	}
	if !recoveryEligibilityTargetRootSnapshotValid(captured) ||
		!recoveryEligibilityTargetRootMatchesBinding(binding, captured) {
		return ErrRecoveryTargetChanged
	}
	if err := lockRecoveryEligibilityTargetRootRowTx(
		ctx, tx, binding.TargetNodeID, binding.TargetRootID,
	); err != nil {
		return recoveryEligibilityTargetRootChanged(ctx)
	}
	root, err := authority.registry.ResolveRecoveryTargetRootTx(
		ctx, tx, binding.TargetNodeID, binding.TargetRootID,
	)
	if err != nil {
		return recoveryEligibilityTargetRootChanged(ctx)
	}
	revisions, err := authority.revisions.ResolveRecoveryNodeRevisionsTx(
		ctx, tx, binding.TargetNodeID, TargetPurposePreflight,
	)
	if err != nil {
		return recoveryEligibilityTargetRootChanged(ctx)
	}
	current := recoveryEligibilityTargetRootSnapshot(root, revisions)
	if !recoveryEligibilityTargetRootSnapshotValid(current) ||
		!recoveryEligibilityTargetRootMatchesBinding(binding, current) || current != captured {
		return ErrRecoveryTargetChanged
	}
	return nil
}

func (authority *recoveryEligibilityTargetRootAuthority) ResolveRecoveryReconciliationRevisionsTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (RecoveryReconciliationRevisionSnapshot, error) {
	if err := recoveryEligibilityTargetRootCallValid(authority, ctx, tx); err != nil ||
		nodeID == 0 || strings.TrimSpace(rootID) == "" || strings.TrimSpace(rootID) != rootID {
		return RecoveryReconciliationRevisionSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	if err := lockRecoveryEligibilityTargetRootRowTx(ctx, tx, nodeID, rootID); err != nil {
		return RecoveryReconciliationRevisionSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	root, err := authority.registry.ResolveRecoveryTargetRootTx(ctx, tx, nodeID, rootID)
	if err != nil {
		return RecoveryReconciliationRevisionSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	revisions, err := authority.revisions.ResolveRecoveryNodeRevisionsTx(
		ctx, tx, nodeID, TargetPurposeReconcile,
	)
	if err != nil {
		return RecoveryReconciliationRevisionSnapshot{}, recoveryEligibilityTargetRootUnavailable(ctx)
	}
	rootSnapshot := recoveryEligibilityTargetRootSnapshot(root, revisions)
	if !recoveryEligibilityTargetRootSnapshotValid(rootSnapshot) ||
		rootSnapshot.NodeID != nodeID || rootSnapshot.RootID != rootID {
		return RecoveryReconciliationRevisionSnapshot{}, ErrRecoveryTargetUnavailable
	}
	result := RecoveryReconciliationRevisionSnapshot{
		NodeRevision: revisions.NodeRevision, CredentialRevision: revisions.CredentialRevision,
		RootRevision: root.AuthorityRevision,
	}
	if !result.valid() {
		return RecoveryReconciliationRevisionSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return result, nil
}

func lockRecoveryEligibilityTargetRootRowTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) error {
	reference := settings.RecoveryTargetRootReference{NodeID: nodeID, RootID: rootID}
	if ctx == nil || tx == nil || settings.ValidateRecoveryTargetRootReference(reference) != nil {
		return ErrRecoveryTargetUnavailable
	}
	key := settings.RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(nodeID), 10) + "." + rootID
	var rows []struct{ Key string }
	loaded := tx.WithContext(ctx).Table("system_settings").Select("key").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("key = ?", key).Limit(2).Find(&rows)
	if loaded.Error != nil || len(rows) != 1 || rows[0].Key != key {
		return recoveryEligibilityTargetRootUnavailable(ctx)
	}
	return nil
}

func recoveryEligibilityTargetRootCallValid(
	authority *recoveryEligibilityTargetRootAuthority,
	ctx context.Context,
	tx *gorm.DB,
) error {
	if ctx == nil || tx == nil || authority == nil || authority.registry == nil || authority.revisions == nil {
		return ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func recoveryEligibilityTargetRootSnapshot(
	root settings.RecoveryTargetRootResolution,
	revisions RecoveryNodeRevisionSnapshot,
) RecoveryEligibilityTargetRootSnapshot {
	return RecoveryEligibilityTargetRootSnapshot{
		NodeID: root.NodeID, RootID: root.RootID, Locator: root.Locator,
		LocatorDigest: root.LocatorDigest, AuthorityRevision: root.AuthorityRevision,
		RootObservationRevision: root.RootObservationRevision, Policy: root.Policy,
		NodeRevision: revisions.NodeRevision, CredentialRevision: revisions.CredentialRevision,
	}
}

func recoveryEligibilityTargetRootSnapshotValid(snapshot RecoveryEligibilityTargetRootSnapshot) bool {
	digest, err := settings.RecoveryTargetRootLocatorDigest(snapshot.NodeID, snapshot.RootID, snapshot.Locator)
	return err == nil && digest == snapshot.LocatorDigest &&
		validOpaqueRevision(snapshot.AuthorityRevision) &&
		validOpaqueRevision(snapshot.RootObservationRevision) &&
		snapshot.Policy.ReserveBytes > 0 && snapshot.Policy.ReserveInodes > 0 &&
		validOpaqueRevision(snapshot.Policy.OverlapPolicyBinding) &&
		validOpaqueRevision(snapshot.NodeRevision) && validOpaqueRevision(snapshot.CredentialRevision)
}

func recoveryEligibilityTargetRootMatchesBinding(
	binding RecoveryAuthorityBinding,
	snapshot RecoveryEligibilityTargetRootSnapshot,
) bool {
	return binding.TargetNodeID == snapshot.NodeID && binding.TargetRootID == snapshot.RootID &&
		binding.RootLocatorDigest == snapshot.LocatorDigest &&
		binding.PreflightNodeRevision == snapshot.NodeRevision &&
		binding.CredentialScopeRevision == snapshot.CredentialRevision
}

func recoveryEligibilityTargetRootUnavailable(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRecoveryTargetUnavailable
}

func recoveryEligibilityTargetRootChanged(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRecoveryTargetChanged
}

var _ RecoveryEligibilityTargetRootPort = (*recoveryEligibilityTargetRootAuthority)(nil)

// The strict target observer is implemented below this durable port. Keeping
// its dependency shape private prevents runtime callers from supplying raw
// locator or host-identity facts.
const targetEligibilityObservationFreshness = 30 * time.Second

type recoveryEligibilityTargetPlanSnapshot struct {
	privateRelativeLocator string
	expiresAt              time.Time
}

func (recoveryEligibilityTargetPlanSnapshot) String() string {
	return "recoveryEligibilityTargetPlanSnapshot{redacted}"
}

func (recoveryEligibilityTargetPlanSnapshot) GoString() string {
	return "recoveryEligibilityTargetPlanSnapshot{redacted}"
}

type recoveryEligibilityTargetPlanSource interface {
	ResolveRecoveryEligibilityTargetPlan(
		context.Context,
		RecoveryEligibilityTargetObservationRequest,
	) (recoveryEligibilityTargetPlanSnapshot, error)
}

type recoveryEligibilityTargetSessionRequest struct {
	nodeID             uint
	nodeRevision       string
	credentialRevision string
	purpose            TargetPurpose
}

func (recoveryEligibilityTargetSessionRequest) String() string {
	return "recoveryEligibilityTargetSessionRequest{redacted}"
}

func (recoveryEligibilityTargetSessionRequest) GoString() string {
	return "recoveryEligibilityTargetSessionRequest{redacted}"
}

type recoveryEligibilityTargetSFTP interface {
	Lstat(string) (os.FileInfo, error)
	RealPath(string) (string, error)
	StatVFS(string) (*sftp.StatVFS, error)
	Close() error
}

type recoveryEligibilityTargetSession struct {
	nodeID                    uint
	nodeRevision              string
	credentialRevision        string
	registeredNodeEndpoint    string
	authenticatedNodeIdentity string
	hostIdentityProof         recoverySourceHostIdentityProof
	protectedRoots            []string
	sftp                      recoveryEligibilityTargetSFTP
	closeSSH                  func() error
	closeOnce                 sync.Once
	closeErr                  error
}

func (*recoveryEligibilityTargetSession) String() string {
	return "recoveryEligibilityTargetSession{redacted}"
}

func (*recoveryEligibilityTargetSession) GoString() string {
	return "recoveryEligibilityTargetSession{redacted}"
}

func (session *recoveryEligibilityTargetSession) close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		if session.sftp != nil {
			session.closeErr = session.sftp.Close()
		}
		if session.closeSSH != nil {
			if err := session.closeSSH(); session.closeErr == nil {
				session.closeErr = err
			}
		}
	})
	return session.closeErr
}

type recoveryEligibilityTargetSessionOpener interface {
	OpenRecoveryEligibilityTarget(
		context.Context,
		recoveryEligibilityTargetSessionRequest,
	) (*recoveryEligibilityTargetSession, error)
}

type recoveryEligibilityTargetObserverDependencies struct {
	Now      func() time.Time
	Plans    recoveryEligibilityTargetPlanSource
	Sessions recoveryEligibilityTargetSessionOpener
}

type recoveryEligibilityTargetObserver struct {
	now      func() time.Time
	plans    recoveryEligibilityTargetPlanSource
	sessions recoveryEligibilityTargetSessionOpener
}

// RecoveryEligibilityTargetObservationDependencies compose only durable plan
// and purpose-exact node/credential authorities. The observer itself always
// creates a strict known_hosts, read-only SSH/SFTP session.
type RecoveryEligibilityTargetObservationDependencies struct {
	DB        *gorm.DB
	Revisions RecoveryNodeRevisionSource
	Now       func() time.Time
}

// NewRecoveryEligibilityTargetObservation constructs the production strict
// target observer. It never accepts a NodeDialer because accept-new and
// insecure host-key modes cannot establish target namespace authority.
func NewRecoveryEligibilityTargetObservation(
	dependencies RecoveryEligibilityTargetObservationDependencies,
) (RecoveryEligibilityTargetObservationPort, error) {
	if dependencies.DB == nil || dependencies.Revisions == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return newRecoveryEligibilityTargetObserverForTest(recoveryEligibilityTargetObserverDependencies{
		Now: dependencies.Now,
		Plans: &recoveryEligibilityTargetGORMPlanSource{
			db: dependencies.DB,
		},
		Sessions: newRecoveryEligibilityTargetProductionSessions(
			dependencies.DB, dependencies.Revisions, dependencies.Now,
		),
	}), nil
}

func newRecoveryEligibilityTargetObserverForTest(
	dependencies recoveryEligibilityTargetObserverDependencies,
) *recoveryEligibilityTargetObserver {
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &recoveryEligibilityTargetObserver{
		now: dependencies.Now, plans: dependencies.Plans, sessions: dependencies.Sessions,
	}
}

func (observer *recoveryEligibilityTargetObserver) ObserveRecoveryEligibilityTarget(
	ctx context.Context,
	request RecoveryEligibilityTargetObservationRequest,
) (RecoveryEligibilityTargetObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryEligibilityTargetObservation{}, ctx.Err()
	}
	if observer == nil || observer.now == nil || observer.plans == nil || observer.sessions == nil ||
		request.Purpose != TargetPurposePreflight || request.RequiredBytes < 0 || request.RequiredInodes < 0 ||
		request.RequiredBytes != request.Binding.RequiredBytes || request.RequiredInodes != request.Binding.RequiredInodes ||
		!recoveryEligibilityTargetRootSnapshotValid(request.TargetRoot) ||
		!recoveryEligibilityTargetRootMatchesBinding(request.Binding, request.TargetRoot) {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	now := observer.now().UTC()
	if now.IsZero() {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	plan, err := observer.plans.ResolveRecoveryEligibilityTargetPlan(ctx, request)
	if err != nil || !validTargetRelativeLocator(plan.privateRelativeLocator) ||
		plan.expiresAt.IsZero() || !plan.expiresAt.UTC().After(now) {
		return RecoveryEligibilityTargetObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	pathDigest, err := TargetPathDigest(
		request.TargetRoot.RootID, request.TargetRoot.LocatorDigest, plan.privateRelativeLocator,
	)
	if err != nil || pathDigest != request.Binding.PathDigest {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetChanged
	}
	session, openErr := observer.sessions.OpenRecoveryEligibilityTarget(ctx, recoveryEligibilityTargetSessionRequest{
		nodeID: request.TargetRoot.NodeID, nodeRevision: request.TargetRoot.NodeRevision,
		credentialRevision: request.TargetRoot.CredentialRevision, purpose: TargetPurposePreflight,
	})
	if session != nil {
		stopWatch, watchDone := watchRecoveryEligibilityTargetContext(ctx, session)
		defer func() {
			close(stopWatch)
			<-watchDone
		}()
	}
	if openErr != nil || session == nil {
		if session != nil {
			_ = session.close()
		}
		return RecoveryEligibilityTargetObservation{}, recoveryEligibilityTargetObservationError(ctx, openErr)
	}
	defer func() { _ = session.close() }()
	expectedIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		session.hostIdentityProof, session.nodeID, session.registeredNodeEndpoint,
	)
	if !identityValid || session.authenticatedNodeIdentity != expectedIdentity || expectedIdentity == "" ||
		session.nodeID != request.TargetRoot.NodeID || session.nodeRevision != request.TargetRoot.NodeRevision ||
		session.credentialRevision != request.TargetRoot.CredentialRevision || session.sftp == nil {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	first, err := observeRecoveryEligibilityTargetState(
		ctx, session.sftp, request, plan.privateRelativeLocator, session.protectedRoots,
	)
	if err != nil {
		return RecoveryEligibilityTargetObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	second, err := observeRecoveryEligibilityTargetState(
		ctx, session.sftp, request, plan.privateRelativeLocator, session.protectedRoots,
	)
	if err != nil {
		return RecoveryEligibilityTargetObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	if !first.sameStableIdentity(second) {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetChanged
	}
	if first.rootRevision != request.Binding.RootRevision ||
		first.rootRevision != request.TargetRoot.RootObservationRevision ||
		first.filesystemRevision != request.Binding.FilesystemRevision ||
		first.targetRevision != request.Binding.PreflightTargetRevision {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetChanged
	}
	if closeErr := session.close(); closeErr != nil {
		return RecoveryEligibilityTargetObservation{}, recoveryEligibilityTargetObservationError(ctx, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return RecoveryEligibilityTargetObservation{}, err
	}
	expiresAt := now.Add(targetEligibilityObservationFreshness)
	if plan.expiresAt.UTC().Before(expiresAt) {
		expiresAt = plan.expiresAt.UTC()
	}
	if !expiresAt.After(now) {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetUnavailable
	}
	return RecoveryEligibilityTargetObservation{
		AuthenticatedNodeIdentity: expectedIdentity, CanonicalRoot: second.canonicalRoot,
		NodeRevision: request.TargetRoot.NodeRevision, CredentialRevision: request.TargetRoot.CredentialRevision,
		RootRevision: second.rootRevision, RootObservationRevision: second.rootRevision,
		FilesystemRevision: second.filesystemRevision, TargetRevision: second.targetRevision,
		FreeBytes: second.freeBytes, FreeInodes: second.freeInodes,
		OverlapsXirangRoot: second.overlapsXirangRoot, ReadOnly: true, Complete: true,
		ObservedAt: now, ExpiresAt: expiresAt,
	}, nil
}

type recoveryEligibilityTargetGORMPlanSource struct {
	db *gorm.DB
}

func (source *recoveryEligibilityTargetGORMPlanSource) ResolveRecoveryEligibilityTargetPlan(
	ctx context.Context,
	request RecoveryEligibilityTargetObservationRequest,
) (recoveryEligibilityTargetPlanSnapshot, error) {
	if source == nil || source.db == nil || ctx == nil || request.Binding.PlanID == "" ||
		request.Binding.PreflightID == "" {
		return recoveryEligibilityTargetPlanSnapshot{}, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return recoveryEligibilityTargetPlanSnapshot{}, err
	}
	var plan model.BackupAssetRecoveryPlan
	var preflight model.BackupAssetRecoveryPreflight
	err := source.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", request.Binding.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil || loaded.RowsAffected != 1 || plan.ID != request.Binding.PlanID {
			return ErrRecoveryTargetUnavailable
		}
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", request.Binding.PreflightID, request.Binding.PlanID).
			Limit(1).Find(&preflight)
		if loaded.Error != nil || loaded.RowsAffected != 1 || preflight.ID != request.Binding.PreflightID ||
			preflight.PlanID != request.Binding.PlanID {
			return ErrRecoveryTargetUnavailable
		}
		return nil
	})
	if err != nil {
		return recoveryEligibilityTargetPlanSnapshot{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	binding := request.Binding
	root := request.TargetRoot
	pathDigest, pathErr := TargetPathDigest(root.RootID, root.LocatorDigest, plan.EncryptedTargetRelativePath)
	if pathErr != nil || pathDigest != binding.PathDigest ||
		plan.BindingDigest != binding.PlanBindingDigest || plan.TransitionRevision != binding.PlanTransitionRevision ||
		plan.TargetNodeID != root.NodeID || plan.TargetRootID != root.RootID ||
		plan.EncryptedTargetRootLocator != root.Locator || plan.RootLocatorDigest != root.LocatorDigest ||
		plan.PathDigest != binding.PathDigest || plan.TargetBaseRevision != binding.TargetBaseRevision ||
		plan.CredentialScopeRevision != binding.CredentialScopeRevision ||
		plan.RootRevision != binding.RootRevision || plan.FilesystemRevision != binding.FilesystemRevision ||
		preflight.Revision != binding.PreflightRevision || preflight.TargetNodeID != root.NodeID ||
		preflight.NodeRevision != binding.PreflightNodeRevision || preflight.TargetRootID != root.RootID ||
		preflight.RootLocatorDigest != root.LocatorDigest || preflight.PathDigest != binding.PathDigest ||
		preflight.TargetRevision != binding.PreflightTargetRevision ||
		preflight.ExpiresAt.IsZero() || !preflight.ExpiresAt.Equal(plan.PreflightExpiresAt) {
		return recoveryEligibilityTargetPlanSnapshot{}, ErrRecoveryTargetChanged
	}
	return recoveryEligibilityTargetPlanSnapshot{
		privateRelativeLocator: plan.EncryptedTargetRelativePath,
		expiresAt:              preflight.ExpiresAt.UTC(),
	}, nil
}

type recoveryEligibilityTargetProductionSessions struct {
	db        *gorm.DB
	revisions RecoveryNodeRevisionSource
	now       func() time.Time
}

type recoveryTargetRootRegistrationSessionOpener interface {
	OpenRecoveryTargetRootRegistration(
		context.Context,
		recoveryEligibilityTargetSessionRequest,
	) (*recoveryEligibilityTargetSession, error)
}

type recoveryTargetRootRegistrationCapture func(
	context.Context,
	TargetRootRegistrationRequest,
) (recoveryEligibilityTargetSessionRequest, error)

type recoveryTargetRootRegistrationProbeDependencies struct {
	Now      func() time.Time
	Capture  recoveryTargetRootRegistrationCapture
	Sessions recoveryTargetRootRegistrationSessionOpener
}

type recoveryTargetRootRegistrationProbe struct {
	now      func() time.Time
	capture  recoveryTargetRootRegistrationCapture
	sessions recoveryTargetRootRegistrationSessionOpener
}

// RecoveryTargetRootRegistrationProbeDependencies compose the durable
// registration-domain revisions with the strict production SSH/SFTP session
// owner. The resulting probe performs read-only observation only.
type RecoveryTargetRootRegistrationProbeDependencies struct {
	DB        *gorm.DB
	Revisions RecoveryNodeRevisionSource
	Now       func() time.Time
}

// NewRecoveryTargetRootRegistrationProbe constructs the production
// purpose-exact target-root registration observer. It deliberately bypasses
// the generic NodeDialer because accept-new and insecure host-key postures are
// not registration authority.
func NewRecoveryTargetRootRegistrationProbe(
	dependencies RecoveryTargetRootRegistrationProbeDependencies,
) (TargetRootRegistrationProbe, error) {
	if dependencies.DB == nil || dependencies.Revisions == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	sessions := newRecoveryEligibilityTargetProductionSessions(
		dependencies.DB, dependencies.Revisions, dependencies.Now,
	)
	capture := func(
		ctx context.Context,
		request TargetRootRegistrationRequest,
	) (recoveryEligibilityTargetSessionRequest, error) {
		if ctx == nil || request.NodeID == 0 {
			return recoveryEligibilityTargetSessionRequest{}, ErrRecoveryTargetUnavailable
		}
		if err := ctx.Err(); err != nil {
			return recoveryEligibilityTargetSessionRequest{}, err
		}
		now := dependencies.Now().UTC()
		if now.IsZero() {
			return recoveryEligibilityTargetSessionRequest{}, ErrRecoveryTargetUnavailable
		}
		var registration recoveryTargetRootAuthorityNodeCredential
		var runtimeRevisions RecoveryNodeRevisionSnapshot
		err := dependencies.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var captureErr error
			registration, captureErr = loadRecoveryTargetRootAuthorityNodeCredential(
				ctx, tx, request.NodeID, now, false,
			)
			if captureErr != nil {
				return captureErr
			}
			runtimeRevisions, captureErr = dependencies.Revisions.ResolveRecoveryNodeRevisionsTx(
				ctx, tx, request.NodeID,
				TargetPurpose(sshutil.PurposeRecoveryTargetRootRegistration),
			)
			return captureErr
		})
		if err != nil || registration.nodeRevision != request.NodeRevision ||
			registration.credentialRevision != request.CredentialRevision ||
			!validOpaqueRevision(runtimeRevisions.NodeRevision) ||
			!validOpaqueRevision(runtimeRevisions.CredentialRevision) {
			return recoveryEligibilityTargetSessionRequest{}, recoveryEligibilityTargetObservationError(ctx, err)
		}
		return recoveryEligibilityTargetSessionRequest{
			nodeID: request.NodeID, nodeRevision: runtimeRevisions.NodeRevision,
			credentialRevision: runtimeRevisions.CredentialRevision,
			purpose:            TargetPurpose(sshutil.PurposeRecoveryTargetRootRegistration),
		}, nil
	}
	return newRecoveryTargetRootRegistrationProbeForTest(
		recoveryTargetRootRegistrationProbeDependencies{
			Now: dependencies.Now, Capture: capture, Sessions: sessions,
		},
	), nil
}

func newRecoveryTargetRootRegistrationProbeForTest(
	dependencies recoveryTargetRootRegistrationProbeDependencies,
) *recoveryTargetRootRegistrationProbe {
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &recoveryTargetRootRegistrationProbe{
		now: dependencies.Now, capture: dependencies.Capture, sessions: dependencies.Sessions,
	}
}

func (probe *recoveryTargetRootRegistrationProbe) ObserveRecoveryTargetRoot(
	ctx context.Context,
	request TargetRootRegistrationRequest,
) (TargetRootRegistrationObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return TargetRootRegistrationObservation{}, err
	}
	if probe == nil || probe.now == nil || probe.capture == nil || probe.sessions == nil ||
		request.Purpose != TargetRootRegistrationPurposeReadOnly || !request.ReadOnly ||
		!validOpaqueRevision(request.NodeRevision) || !validOpaqueRevision(request.CredentialRevision) ||
		validateTargetRootRegistrationRequest(request) != nil {
		return TargetRootRegistrationObservation{}, ErrRecoveryTargetUnavailable
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		request.NodeID, request.RootID, request.Locator,
	)
	if err != nil {
		return TargetRootRegistrationObservation{}, ErrRecoveryTargetUnavailable
	}
	sessionRequest, err := probe.capture(ctx, request)
	if err != nil || sessionRequest.nodeID != request.NodeID ||
		sessionRequest.purpose != TargetPurpose(sshutil.PurposeRecoveryTargetRootRegistration) ||
		!validOpaqueRevision(sessionRequest.nodeRevision) ||
		!validOpaqueRevision(sessionRequest.credentialRevision) {
		return TargetRootRegistrationObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	session, openErr := probe.sessions.OpenRecoveryTargetRootRegistration(ctx, sessionRequest)
	if session != nil {
		stopWatch, watchDone := watchRecoveryEligibilityTargetContext(ctx, session)
		defer func() {
			close(stopWatch)
			<-watchDone
		}()
	}
	if openErr != nil || session == nil {
		if session != nil {
			_ = session.close()
		}
		return TargetRootRegistrationObservation{}, recoveryEligibilityTargetObservationError(ctx, openErr)
	}
	defer func() { _ = session.close() }()
	expectedIdentity, identityValid := recoverySourceAuthenticatedNodeIdentity(
		session.hostIdentityProof, session.nodeID, session.registeredNodeEndpoint,
	)
	if !identityValid || expectedIdentity == "" || session.authenticatedNodeIdentity != expectedIdentity ||
		session.nodeID != request.NodeID || session.nodeRevision != sessionRequest.nodeRevision ||
		session.credentialRevision != sessionRequest.credentialRevision || session.sftp == nil {
		return TargetRootRegistrationObservation{}, ErrRecoveryTargetUnavailable
	}
	first, err := observeRecoveryTargetRootRegistrationState(ctx, session.sftp, request, locatorDigest)
	if err != nil {
		return TargetRootRegistrationObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	second, err := observeRecoveryTargetRootRegistrationState(ctx, session.sftp, request, locatorDigest)
	if err != nil {
		return TargetRootRegistrationObservation{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	if first != second {
		return TargetRootRegistrationObservation{}, ErrRecoveryTargetChanged
	}
	if closeErr := session.close(); closeErr != nil {
		return TargetRootRegistrationObservation{}, recoveryEligibilityTargetObservationError(ctx, closeErr)
	}
	if err := ctx.Err(); err != nil {
		return TargetRootRegistrationObservation{}, err
	}
	observedAt := probe.now().UTC()
	if observedAt.IsZero() {
		return TargetRootRegistrationObservation{}, ErrRecoveryTargetUnavailable
	}
	return TargetRootRegistrationObservation{
		NodeID: request.NodeID, RootID: request.RootID, LocatorDigest: locatorDigest,
		NodeRevision: request.NodeRevision, CredentialRevision: request.CredentialRevision,
		RootObservationRevision: second.rootObservationRevision,
		Purpose:                 TargetRootRegistrationPurposeReadOnly, ReadOnly: true, ObservedAt: observedAt,
	}, nil
}

type recoveryTargetRootRegistrationState struct {
	canonicalRoot           string
	rootObservationRevision string
}

func observeRecoveryTargetRootRegistrationState(
	ctx context.Context,
	client recoveryEligibilityTargetSFTP,
	request TargetRootRegistrationRequest,
	locatorDigest string,
) (recoveryTargetRootRegistrationState, error) {
	if ctx == nil || client == nil || locatorDigest == "" {
		return recoveryTargetRootRegistrationState{}, ErrRecoveryTargetUnavailable
	}
	rootInfo, canonicalRoot, err := observeRecoveryEligibilityCanonicalDirectory(
		ctx, client, request.Locator,
	)
	if err != nil || rootInfo == nil || canonicalRoot != request.Locator ||
		rootInfo.Mode().Perm()&0o002 != 0 {
		return recoveryTargetRootRegistrationState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	uid, gid, ok := recoverySFTPFileOwner(rootInfo)
	if !ok {
		return recoveryTargetRootRegistrationState{}, ErrRecoveryTargetUnavailable
	}
	rootVFS, err := client.StatVFS(canonicalRoot)
	if err != nil || rootVFS == nil || rootVFS.Fsid == 0 {
		return recoveryTargetRootRegistrationState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	parentVFS, err := client.StatVFS(path.Dir(canonicalRoot))
	if err != nil || parentVFS == nil || parentVFS.Fsid != rootVFS.Fsid {
		return recoveryTargetRootRegistrationState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	revision, err := recoverySFTPRootObservationRevision(recoveryTargetPreflightSessionBinding{
		nodeID: request.NodeID, rootID: request.RootID,
		rootLocator: canonicalRoot, rootLocatorDigest: locatorDigest,
	}, rootInfo.Mode(), uid, gid, rootVFS.Fsid)
	if err != nil {
		return recoveryTargetRootRegistrationState{}, ErrRecoveryTargetUnavailable
	}
	return recoveryTargetRootRegistrationState{
		canonicalRoot: canonicalRoot, rootObservationRevision: revision,
	}, nil
}

func newRecoveryEligibilityTargetProductionSessions(
	db *gorm.DB,
	revisions RecoveryNodeRevisionSource,
	now func() time.Time,
) *recoveryEligibilityTargetProductionSessions {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &recoveryEligibilityTargetProductionSessions{db: db, revisions: revisions, now: now}
}

func (sessions *recoveryEligibilityTargetProductionSessions) OpenRecoveryEligibilityTarget(
	ctx context.Context,
	request recoveryEligibilityTargetSessionRequest,
) (*recoveryEligibilityTargetSession, error) {
	if request.purpose != TargetPurposePreflight {
		return nil, ErrRecoveryTargetUnavailable
	}
	return sessions.openRecoveryEligibilityTarget(ctx, request)
}

func (sessions *recoveryEligibilityTargetProductionSessions) OpenRecoveryTargetRootRegistration(
	ctx context.Context,
	request recoveryEligibilityTargetSessionRequest,
) (*recoveryEligibilityTargetSession, error) {
	if request.purpose != TargetPurpose(sshutil.PurposeRecoveryTargetRootRegistration) {
		return nil, ErrRecoveryTargetUnavailable
	}
	return sessions.openRecoveryEligibilityTarget(ctx, request)
}

func (sessions *recoveryEligibilityTargetProductionSessions) openRecoveryEligibilityTarget(
	ctx context.Context,
	request recoveryEligibilityTargetSessionRequest,
) (*recoveryEligibilityTargetSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessions == nil || sessions.db == nil || sessions.revisions == nil || sessions.now == nil ||
		request.nodeID == 0 ||
		!validOpaqueRevision(request.nodeRevision) || !validOpaqueRevision(request.credentialRevision) {
		return nil, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := sessions.now().UTC()
	if now.IsZero() {
		return nil, ErrRecoveryTargetUnavailable
	}
	var node model.Node
	var revisions RecoveryNodeRevisionSnapshot
	err := sessions.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND archived = ?", request.nodeID, false).Limit(1).Find(&node)
		if loaded.Error != nil || loaded.RowsAffected != 1 || node.ID != request.nodeID || node.Archived {
			return ErrRecoveryTargetUnavailable
		}
		var revisionErr error
		revisions, revisionErr = sessions.revisions.ResolveRecoveryNodeRevisionsTx(
			ctx, tx, request.nodeID, request.purpose,
		)
		return revisionErr
	})
	if err != nil || revisions.NodeRevision != request.nodeRevision ||
		revisions.CredentialRevision != request.credentialRevision ||
		strings.TrimSpace(node.Host) == "" || strings.TrimSpace(node.Username) == "" ||
		strings.ToLower(strings.TrimSpace(node.AuthType)) != "key" {
		return nil, recoveryEligibilityTargetObservationError(ctx, err)
	}
	auth, _, err := sshutil.BuildSSHAuthForPurpose(node, sessions.db, string(request.purpose))
	if err != nil || len(auth) == 0 {
		return nil, ErrRecoveryTargetUnavailable
	}
	rawKnownHosts := strings.TrimSpace(util.GetEnvOrDefault("SSH_KNOWN_HOSTS_PATH", "~/.ssh/known_hosts"))
	knownHostsPath, err := util.ExpandHomePath(rawKnownHosts)
	if err != nil || strings.TrimSpace(knownHostsPath) == "" {
		return nil, ErrRecoveryTargetUnavailable
	}
	verifier, err := newRecoverySourceStrictKnownHostVerifier(knownHostsPath)
	if err != nil || verifier == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	port := node.Port
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return nil, ErrRecoveryTargetUnavailable
	}
	endpoint := net.JoinHostPort(strings.TrimSpace(node.Host), strconv.Itoa(port))
	connection, err := sshutil.DialSSH(
		ctx, endpoint, strings.TrimSpace(node.Username), auth, verifier.Verify,
	)
	if err != nil || connection == nil {
		if connection != nil {
			_ = connection.Close()
		}
		return nil, recoveryEligibilityTargetObservationError(ctx, err)
	}
	proof := verifier.Proof()
	identity, validIdentity := recoverySourceAuthenticatedNodeIdentity(proof, node.ID, endpoint)
	if !validIdentity {
		_ = connection.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	client, err := sftp.NewClient(connection)
	if err != nil || client == nil {
		if client != nil {
			_ = client.Close()
		}
		_ = connection.Close()
		return nil, recoveryEligibilityTargetObservationError(ctx, err)
	}
	protectedRoots, ok := recoveryEligibilityTargetProtectedRoots(node)
	if !ok {
		_ = client.Close()
		_ = connection.Close()
		return nil, ErrRecoveryTargetUnavailable
	}
	return &recoveryEligibilityTargetSession{
		nodeID: node.ID, nodeRevision: revisions.NodeRevision,
		credentialRevision: request.credentialRevision, registeredNodeEndpoint: endpoint,
		authenticatedNodeIdentity: identity, hostIdentityProof: proof,
		protectedRoots: protectedRoots, sftp: client, closeSSH: connection.Close,
	}, nil
}

func recoveryEligibilityTargetProtectedRoots(node model.Node) ([]string, bool) {
	base := strings.TrimSpace(node.BasePath)
	backup := strings.TrimSpace(node.BackupDir)
	result := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	appendRoot := func(value string) bool {
		if value == "" {
			return true
		}
		if !strings.HasPrefix(value, "/") || path.Clean(value) != value {
			return false
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
		return true
	}
	if !appendRoot(base) {
		return nil, false
	}
	if backup != "" && !strings.HasPrefix(backup, "/") {
		if base == "" {
			return nil, false
		}
		backup = path.Join(base, backup)
	}
	if !appendRoot(backup) {
		return nil, false
	}
	return result, true
}

type recoveryEligibilityTargetState struct {
	canonicalRoot      string
	rootRevision       string
	filesystemRevision string
	targetRevision     string
	freeBytes          int64
	freeInodes         int64
	overlapsXirangRoot bool
}

func (state recoveryEligibilityTargetState) sameStableIdentity(other recoveryEligibilityTargetState) bool {
	return state.canonicalRoot == other.canonicalRoot && state.rootRevision == other.rootRevision &&
		state.filesystemRevision == other.filesystemRevision && state.targetRevision == other.targetRevision &&
		state.overlapsXirangRoot == other.overlapsXirangRoot
}

func observeRecoveryEligibilityTargetState(
	ctx context.Context,
	client recoveryEligibilityTargetSFTP,
	request RecoveryEligibilityTargetObservationRequest,
	privateRelativeLocator string,
	protectedRoots []string,
) (recoveryEligibilityTargetState, error) {
	if client == nil || ctx == nil || request.TargetRoot.Locator == "" ||
		!validTargetRelativeLocator(privateRelativeLocator) {
		return recoveryEligibilityTargetState{}, ErrRecoveryTargetUnavailable
	}
	rootInfo, canonicalRoot, err := observeRecoveryEligibilityCanonicalDirectory(
		ctx, client, request.TargetRoot.Locator,
	)
	if err != nil || rootInfo == nil || canonicalRoot != request.TargetRoot.Locator ||
		rootInfo.Mode().Perm()&0o002 != 0 {
		return recoveryEligibilityTargetState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	rootUID, rootGID, ok := recoverySFTPFileOwner(rootInfo)
	if !ok {
		return recoveryEligibilityTargetState{}, ErrRecoveryTargetUnavailable
	}
	rootVFS, err := client.StatVFS(canonicalRoot)
	if err != nil || rootVFS == nil || rootVFS.Fsid == 0 {
		return recoveryEligibilityTargetState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	parentVFS, err := client.StatVFS(path.Dir(canonicalRoot))
	if err != nil || parentVFS == nil || parentVFS.Fsid != rootVFS.Fsid {
		return recoveryEligibilityTargetState{}, recoveryEligibilityTargetObservationError(ctx, err)
	}
	freeBytes, ok := recoveryAvailableBytes(rootVFS.Bavail, rootVFS.Frsize)
	if !ok || rootVFS.Favail > uint64(math.MaxInt64) {
		return recoveryEligibilityTargetState{}, ErrRecoveryTargetUnavailable
	}
	revisionBinding := recoveryTargetPreflightSessionBinding{
		nodeID: request.TargetRoot.NodeID, rootID: request.TargetRoot.RootID,
		rootLocator: canonicalRoot, rootLocatorDigest: request.TargetRoot.LocatorDigest,
	}
	rootRevision, err := recoverySFTPRootObservationRevision(
		revisionBinding, rootInfo.Mode(), rootUID, rootGID, rootVFS.Fsid,
	)
	if err != nil {
		return recoveryEligibilityTargetState{}, ErrRecoveryTargetUnavailable
	}
	filesystemRevision, err := recoverySFTPFilesystemObservationRevision(rootVFS)
	if err != nil {
		return recoveryEligibilityTargetState{}, ErrRecoveryTargetUnavailable
	}
	targetRevision, err := observeRecoveryEligibilityTargetPath(
		ctx, client, canonicalRoot, privateRelativeLocator, rootRevision, rootVFS.Fsid,
	)
	if err != nil {
		return recoveryEligibilityTargetState{}, err
	}
	overlaps := false
	for _, protectedRoot := range protectedRoots {
		_, canonicalProtected, observeErr := observeRecoveryEligibilityCanonicalDirectory(
			ctx, client, protectedRoot,
		)
		if observeErr != nil {
			return recoveryEligibilityTargetState{}, observeErr
		}
		if fileaccess.Contains(canonicalProtected, canonicalRoot) || fileaccess.Contains(canonicalRoot, canonicalProtected) {
			overlaps = true
		}
	}
	return recoveryEligibilityTargetState{
		canonicalRoot: canonicalRoot, rootRevision: rootRevision,
		filesystemRevision: filesystemRevision, targetRevision: targetRevision,
		freeBytes: freeBytes, freeInodes: int64(rootVFS.Favail), overlapsXirangRoot: overlaps,
	}, nil
}

func observeRecoveryEligibilityCanonicalDirectory(
	ctx context.Context,
	client recoveryEligibilityTargetSFTP,
	value string,
) (os.FileInfo, string, error) {
	prefixes, ok := recoveryAbsolutePathPrefixes(value)
	if !ok || client == nil {
		return nil, "", ErrRecoveryTargetUnavailable
	}
	var result os.FileInfo
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		info, err := client.Lstat(prefix)
		if err != nil || info == nil {
			return nil, "", recoveryEligibilityTargetObservationError(ctx, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, "", ErrRecoveryTargetChanged
		}
		canonical, err := client.RealPath(prefix)
		if err != nil || canonical != prefix {
			return nil, "", recoveryEligibilityTargetObservationError(ctx, err)
		}
		result = info
	}
	return result, value, nil
}

func observeRecoveryEligibilityTargetPath(
	ctx context.Context,
	client recoveryEligibilityTargetSFTP,
	root string,
	privateRelativeLocator string,
	rootRevision string,
	rootFilesystemID uint64,
) (string, error) {
	components := strings.Split(privateRelativeLocator, "/")
	current := root
	for index, component := range components {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		current = path.Join(current, component)
		info, err := client.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return recoverySFTPTargetAbsentRevision(rootRevision, privateRelativeLocator)
			}
			return "", recoveryEligibilityTargetObservationError(ctx, err)
		}
		if info == nil || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrRecoveryTargetChanged
		}
		canonical, err := client.RealPath(current)
		if err != nil || canonical != current {
			return "", recoveryEligibilityTargetObservationError(ctx, err)
		}
		filesystem, err := client.StatVFS(current)
		if err != nil || filesystem == nil || filesystem.Fsid != rootFilesystemID {
			return "", recoveryEligibilityTargetObservationError(ctx, err)
		}
		if index != len(components)-1 {
			if !info.IsDir() {
				return "", ErrRecoveryTargetChanged
			}
			continue
		}
		return recoverySFTPTargetPresentRevision(rootRevision, privateRelativeLocator, info)
	}
	return "", ErrRecoveryTargetUnavailable
}

func watchRecoveryEligibilityTargetContext(
	ctx context.Context,
	session *recoveryEligibilityTargetSession,
) (chan struct{}, chan struct{}) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = session.close()
		case <-stop:
		}
	}()
	return stop, done
}

func recoveryEligibilityTargetObservationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrRecoveryTargetChanged) {
		return ErrRecoveryTargetChanged
	}
	return ErrRecoveryTargetUnavailable
}

var _ RecoveryEligibilityTargetObservationPort = (*recoveryEligibilityTargetObserver)(nil)
var _ TargetRootRegistrationProbe = (*recoveryTargetRootRegistrationProbe)(nil)
var _ recoveryEligibilityTargetPlanSource = (*recoveryEligibilityTargetGORMPlanSource)(nil)
var _ recoveryEligibilityTargetSessionOpener = (*recoveryEligibilityTargetProductionSessions)(nil)
var _ recoveryTargetRootRegistrationSessionOpener = (*recoveryEligibilityTargetProductionSessions)(nil)
