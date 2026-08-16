package recovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// RecoveryEligibilitySourceObservation is the closed Repository scalar that
// accompanies one transferred managed-Rsync source capability. It contains no
// locator or canonical namespace; Recovery obtains those only from the opaque
// RecoverySourceNamespaceObservation capability that Repository transfers.
type RecoveryEligibilitySourceObservation struct {
	RepositoryCapabilityRevision int    `json:"-"`
	CapabilityRevision           int    `json:"-"`
	SourceAccessIdentity         string `json:"-"`
	SourceFingerprint            string `json:"-"`
	ManagedRootIdentity          string `json:"-"`
	RepositoryBindingRevision    string `json:"-"`
	ProvenanceRevision           string `json:"-"`
}

func (RecoveryEligibilitySourceObservation) String() string {
	return "RecoveryEligibilitySourceObservation{redacted}"
}

func (RecoveryEligibilitySourceObservation) GoString() string {
	return "RecoveryEligibilitySourceObservation{redacted}"
}

// RecoveryEligibilitySourcePort is implemented at the runtime composition
// boundary over Repository. A successful Observe transfers ownership of the
// returned source capability to Recovery, which must close it exactly once.
// External access occurs only in Observe; the second method may perform
// durable checks only in the caller-owned tx.
type RecoveryEligibilitySourcePort interface {
	ObserveRecoveryEligibilitySource(
		context.Context,
		provider.RecoverySourceAuthorityRequest,
	) (provider.RsyncRestoreSource, RecoveryEligibilitySourceObservation, error)
	RevalidateRecoveryEligibilitySourceTx(
		context.Context,
		*gorm.DB,
		RecoveryAuthorityBinding,
		RecoveryEligibilitySourceObservation,
	) error
}

// RecoveryEligibilitySecurityObservation is the closed plan-level Processing
// product. Raw artifacts, malware bytes, filenames, and per-asset inputs never
// cross this boundary.
type RecoveryEligibilitySecurityObservation struct {
	PolicyRevision   string                     `json:"-"`
	FindingSetDigest string                     `json:"-"`
	Disposition      SecurityFindingDisposition `json:"-"`
	Complete         bool                       `json:"-"`
	ObservedAt       time.Time                  `json:"-"`
}

func (RecoveryEligibilitySecurityObservation) String() string {
	return "RecoveryEligibilitySecurityObservation{redacted}"
}

func (RecoveryEligibilitySecurityObservation) GoString() string {
	return "RecoveryEligibilitySecurityObservation{redacted}"
}

// RecoveryEligibilitySecurityPort is implemented by the runtime's narrow
// Processing adapter. Observation is external to the caller transaction;
// revalidation may inspect only durable current security state in that tx.
type RecoveryEligibilitySecurityPort interface {
	ObserveRecoveryEligibilitySecurity(
		context.Context,
		RecoveryAuthorityBinding,
	) (RecoveryEligibilitySecurityObservation, error)
	RevalidateRecoveryEligibilitySecurityTx(
		context.Context,
		*gorm.DB,
		RecoveryAuthorityBinding,
		RecoveryEligibilitySecurityObservation,
	) error
}

// RecoveryEligibilityTargetRootSnapshot is the exact private v2 registry,
// node, credential, and policy snapshot captured in the first short tx.
type RecoveryEligibilityTargetRootSnapshot struct {
	NodeID                  uint                              `json:"-"`
	RootID                  string                            `json:"-"`
	Locator                 string                            `json:"-"`
	LocatorDigest           string                            `json:"-"`
	AuthorityRevision       string                            `json:"-"`
	RootObservationRevision string                            `json:"-"`
	Policy                  settings.RecoveryTargetRootPolicy `json:"-"`
	NodeRevision            string                            `json:"-"`
	CredentialRevision      string                            `json:"-"`
}

func (RecoveryEligibilityTargetRootSnapshot) String() string {
	return "RecoveryEligibilityTargetRootSnapshot{redacted}"
}

func (RecoveryEligibilityTargetRootSnapshot) GoString() string {
	return "RecoveryEligibilityTargetRootSnapshot{redacted}"
}

// RecoveryEligibilityTargetRootPort owns durable v2 root and current
// node/credential revisions. It also supplies the same owner's reconciliation
// projection; locator digest and remote observation revision are never used as
// substitutes for AuthorityRevision.
type RecoveryEligibilityTargetRootPort interface {
	CaptureRecoveryEligibilityTargetRootTx(
		context.Context,
		*gorm.DB,
		RecoveryAuthorityBinding,
	) (RecoveryEligibilityTargetRootSnapshot, error)
	RevalidateRecoveryEligibilityTargetRootTx(
		context.Context,
		*gorm.DB,
		RecoveryAuthorityBinding,
		RecoveryEligibilityTargetRootSnapshot,
	) error
	ResolveRecoveryReconciliationRevisionsTx(
		context.Context,
		*gorm.DB,
		uint,
		string,
	) (RecoveryReconciliationRevisionSnapshot, error)
}

// RecoveryEligibilityTargetObservationRequest transfers the exact private
// root snapshot to a purpose-exact, read-only target observer.
type RecoveryEligibilityTargetObservationRequest struct {
	Binding        RecoveryAuthorityBinding              `json:"-"`
	TargetRoot     RecoveryEligibilityTargetRootSnapshot `json:"-"`
	Purpose        TargetPurpose                         `json:"-"`
	RequiredBytes  int64                                 `json:"-"`
	RequiredInodes int64                                 `json:"-"`
}

func (RecoveryEligibilityTargetObservationRequest) String() string {
	return "RecoveryEligibilityTargetObservationRequest{redacted}"
}

func (RecoveryEligibilityTargetObservationRequest) GoString() string {
	return "RecoveryEligibilityTargetObservationRequest{redacted}"
}

// RecoveryEligibilityTargetObservation is the sealed target-side namespace,
// revision, and capacity evidence. CanonicalRoot and authenticated identity
// remain Recovery-private inputs to the overlap decision.
type RecoveryEligibilityTargetObservation struct {
	AuthenticatedNodeIdentity string    `json:"-"`
	CanonicalRoot             string    `json:"-"`
	NodeRevision              string    `json:"-"`
	CredentialRevision        string    `json:"-"`
	RootRevision              string    `json:"-"`
	RootObservationRevision   string    `json:"-"`
	FilesystemRevision        string    `json:"-"`
	TargetRevision            string    `json:"-"`
	FreeBytes                 int64     `json:"-"`
	FreeInodes                int64     `json:"-"`
	OverlapsXirangRoot        bool      `json:"-"`
	ReadOnly                  bool      `json:"-"`
	Complete                  bool      `json:"-"`
	ObservedAt                time.Time `json:"-"`
	ExpiresAt                 time.Time `json:"-"`
}

func (RecoveryEligibilityTargetObservation) String() string {
	return "RecoveryEligibilityTargetObservation{redacted}"
}

func (RecoveryEligibilityTargetObservation) GoString() string {
	return "RecoveryEligibilityTargetObservation{redacted}"
}

type RecoveryEligibilityTargetObservationPort interface {
	ObserveRecoveryEligibilityTarget(
		context.Context,
		RecoveryEligibilityTargetObservationRequest,
	) (RecoveryEligibilityTargetObservation, error)
}

// RecoveryAuthorityObservation is the sole sealed eligibility product passed
// from external observation into a later caller-owned transaction.
type RecoveryAuthorityObservation struct {
	observedAt time.Time
	expiresAt  time.Time
	binding    recoveryEligibilityBinding
	proof      *recoveryEligibilityProof
}

func (RecoveryAuthorityBinding) String() string {
	return "RecoveryAuthorityBinding{redacted}"
}

func (RecoveryAuthorityBinding) GoString() string {
	return "RecoveryAuthorityBinding{redacted}"
}

func (RecoveryAuthorityObservation) String() string {
	return "RecoveryAuthorityObservation{redacted}"
}

func (RecoveryAuthorityObservation) GoString() string {
	return "RecoveryAuthorityObservation{redacted}"
}

type recoveryEligibilityBinding struct {
	authority          RecoveryAuthorityBinding
	source             RecoveryEligibilitySourceObservation
	sourceNamespace    *RecoverySourceNamespaceObservation
	security           RecoveryEligibilitySecurityObservation
	targetRoot         RecoveryEligibilityTargetRootSnapshot
	target             RecoveryEligibilityTargetObservation
	sourceAccessible   bool
	overlapsXirangRoot bool
	overlapsSourceRoot bool
	reservedBytes      int64
	reservedInodes     int64
	findingDisposition SecurityFindingDisposition
}

func (recoveryEligibilityBinding) String() string {
	return "recoveryEligibilityBinding{redacted}"
}

func (recoveryEligibilityBinding) GoString() string {
	return "recoveryEligibilityBinding{redacted}"
}

type recoveryEligibilityProof struct {
	bindingDigest string
	production    bool
}

func (recoveryEligibilityProof) String() string {
	return "recoveryEligibilityProof{redacted}"
}

func (recoveryEligibilityProof) GoString() string {
	return "recoveryEligibilityProof{redacted}"
}

type RecoveryEligibilityAuthorityDependencies struct {
	DB                *gorm.DB
	Source            RecoveryEligibilitySourcePort
	Security          RecoveryEligibilitySecurityPort
	TargetRoot        RecoveryEligibilityTargetRootPort
	TargetObservation RecoveryEligibilityTargetObservationPort
	Now               func() time.Time
}

// RecoveryEligibilityAuthority is the single owner projected through the
// live, preflight, and reconciliation interfaces. B4 freezes this contract;
// B5/B6 supply and verify the complete two-phase implementation.
type RecoveryEligibilityAuthority struct {
	db                *gorm.DB
	source            RecoveryEligibilitySourcePort
	security          RecoveryEligibilitySecurityPort
	targetRoot        RecoveryEligibilityTargetRootPort
	targetObservation RecoveryEligibilityTargetObservationPort
	now               func() time.Time
}

func NewRecoveryEligibilityAuthority(
	dependencies RecoveryEligibilityAuthorityDependencies,
) (*RecoveryEligibilityAuthority, error) {
	if dependencies.DB == nil || dependencies.Source == nil || dependencies.Security == nil ||
		dependencies.TargetRoot == nil || dependencies.TargetObservation == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &RecoveryEligibilityAuthority{
		db: dependencies.DB, source: dependencies.Source, security: dependencies.Security,
		targetRoot: dependencies.TargetRoot, targetObservation: dependencies.TargetObservation,
		now: dependencies.Now,
	}, nil
}

func (authority *RecoveryEligibilityAuthority) ObserveRecoveryAuthority(
	ctx context.Context,
	binding RecoveryAuthorityBinding,
) (result RecoveryAuthorityObservation, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryAuthorityObservation{}, err
	}
	if authority == nil || authority.db == nil || authority.source == nil || authority.security == nil ||
		authority.targetRoot == nil || authority.targetObservation == nil || authority.now == nil ||
		!validRecoveryEligibilityAuthorityBinding(binding) {
		return RecoveryAuthorityObservation{}, ErrRecoveryTargetUnavailable
	}
	// Provider coverage is capability-exact. Unsupported providers stop before
	// any durable owner or external source/target observer is invoked.
	if binding.Provider != backupasset.ProviderRsync {
		return RecoveryAuthorityObservation{}, ErrRecoveryTargetUnavailable
	}

	var targetRoot RecoveryEligibilityTargetRootSnapshot
	err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var captureErr error
		targetRoot, captureErr = authority.targetRoot.CaptureRecoveryEligibilityTargetRootTx(
			ctx, tx, binding,
		)
		return captureErr
	})
	if err != nil || !validRecoveryEligibilityTargetRoot(binding, targetRoot) {
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}

	sourceCapability, sourceObservation, err := authority.source.ObserveRecoveryEligibilitySource(
		ctx,
		provider.RecoverySourceAuthorityRequest{Provider: binding.Provider, RsyncRef: binding.SourceRef},
	)
	if err != nil || sourceCapability == nil {
		if sourceCapability != nil {
			_ = sourceCapability.Close()
		}
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}
	defer func() {
		if closeErr := sourceCapability.Close(); closeErr != nil && resultErr == nil {
			result = RecoveryAuthorityObservation{}
			resultErr = recoveryEligibilityObservationError(ctx, closeErr)
		}
	}()
	if err := sourceCapability.Revalidate(ctx); err != nil {
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}
	sourceNamespace, ok := sourceCapability.(*RecoverySourceNamespaceObservation)
	if !ok || !validRecoveryEligibilitySource(binding, sourceObservation, sourceNamespace, authority.now().UTC()) {
		return RecoveryAuthorityObservation{}, ErrRecoveryTargetUnavailable
	}

	securityObservation, err := authority.security.ObserveRecoveryEligibilitySecurity(ctx, binding)
	if err != nil || !validRecoveryEligibilitySecurity(binding, securityObservation, authority.now().UTC()) {
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}

	targetObservation, err := authority.targetObservation.ObserveRecoveryEligibilityTarget(
		ctx,
		RecoveryEligibilityTargetObservationRequest{
			Binding: binding, TargetRoot: targetRoot, Purpose: TargetPurposePreflight,
			RequiredBytes: binding.RequiredBytes, RequiredInodes: binding.RequiredInodes,
		},
	)
	now := authority.now().UTC()
	if err != nil || !validRecoveryEligibilityTarget(binding, targetRoot, targetObservation, now) {
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}

	sourceProof := sourceNamespace.observation.proof
	overlapsSourceRoot := false
	if sourceProof.authenticatedNodeIdentity == targetObservation.AuthenticatedNodeIdentity {
		overlapsSourceRoot = fileaccess.Contains(sourceProof.canonicalPath, targetObservation.CanonicalRoot) ||
			fileaccess.Contains(targetObservation.CanonicalRoot, sourceProof.canonicalPath)
	}
	closed := recoveryEligibilityBinding{
		authority: binding, source: sourceObservation, security: securityObservation,
		sourceNamespace: sourceNamespace, targetRoot: targetRoot, target: targetObservation,
		sourceAccessible: true, overlapsXirangRoot: targetObservation.OverlapsXirangRoot,
		overlapsSourceRoot: overlapsSourceRoot,
		reservedBytes:      targetRoot.Policy.ReserveBytes, reservedInodes: targetRoot.Policy.ReserveInodes,
		findingDisposition: securityObservation.Disposition,
	}

	err = authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if revalidateErr := authority.targetRoot.RevalidateRecoveryEligibilityTargetRootTx(
			ctx, tx, binding, targetRoot,
		); revalidateErr != nil {
			return revalidateErr
		}
		if revalidateErr := sourceNamespace.revalidateTx(ctx, tx); revalidateErr != nil {
			return revalidateErr
		}
		if revalidateErr := authority.source.RevalidateRecoveryEligibilitySourceTx(
			ctx, tx, binding, sourceObservation,
		); revalidateErr != nil {
			return revalidateErr
		}
		return authority.security.RevalidateRecoveryEligibilitySecurityTx(
			ctx, tx, binding, securityObservation,
		)
	})
	if err != nil {
		return RecoveryAuthorityObservation{}, recoveryEligibilityObservationError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return RecoveryAuthorityObservation{}, err
	}
	now = authority.now().UTC()
	if now.IsZero() || now.Before(targetObservation.ObservedAt.UTC()) ||
		!now.Before(targetObservation.ExpiresAt.UTC()) {
		return RecoveryAuthorityObservation{}, ErrRecoveryTargetChanged
	}
	proof := &recoveryEligibilityProof{production: true}
	proof.bindingDigest = recoveryEligibilityBindingDigest(closed)
	if proof.bindingDigest == "" {
		return RecoveryAuthorityObservation{}, ErrRecoveryTargetUnavailable
	}
	return RecoveryAuthorityObservation{
		observedAt: targetObservation.ObservedAt.UTC(), expiresAt: targetObservation.ExpiresAt.UTC(),
		binding: closed, proof: proof,
	}, nil
}

func (authority *RecoveryEligibilityAuthority) RevalidateRecoveryAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	binding RecoveryAuthorityBinding,
	observation RecoveryAuthorityObservation,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if authority == nil || authority.now == nil || authority.source == nil || authority.security == nil ||
		authority.targetRoot == nil || tx == nil || observation.proof == nil || !observation.proof.production ||
		observation.binding.authority != binding || !validRecoveryEligibilityAuthorityBinding(binding) ||
		!validRecoveryEligibilitySourceNamespaceBinding(observation.binding) ||
		observation.observedAt.IsZero() || observation.expiresAt.IsZero() ||
		!observation.observedAt.Before(observation.expiresAt) ||
		observation.proof.bindingDigest == "" ||
		observation.proof.bindingDigest != recoveryEligibilityBindingDigest(observation.binding) {
		return ErrRecoveryTargetChanged
	}
	now := authority.now().UTC()
	if now.IsZero() || now.Before(observation.observedAt.UTC()) || !now.Before(observation.expiresAt.UTC()) ||
		!observation.binding.sourceAccessible || observation.binding.reservedBytes <= 0 ||
		observation.binding.reservedInodes <= 0 || observation.binding.overlapsXirangRoot ||
		observation.binding.overlapsSourceRoot || !observation.binding.findingDisposition.valid() ||
		!recoveryEligibilityCapacityAvailable(
			observation.binding.target.FreeBytes, binding.RequiredBytes, observation.binding.reservedBytes,
		) || !recoveryEligibilityCapacityAvailable(
		observation.binding.target.FreeInodes, binding.RequiredInodes, observation.binding.reservedInodes,
	) {
		return ErrRecoveryTargetChanged
	}
	if err := authority.targetRoot.RevalidateRecoveryEligibilityTargetRootTx(
		ctx, tx, binding, observation.binding.targetRoot,
	); err != nil {
		return recoveryEligibilityRevalidationError(ctx, err)
	}
	if err := observation.binding.sourceNamespace.revalidateTx(ctx, tx); err != nil {
		return recoveryEligibilityRevalidationError(ctx, err)
	}
	if err := authority.source.RevalidateRecoveryEligibilitySourceTx(
		ctx, tx, binding, observation.binding.source,
	); err != nil {
		return recoveryEligibilityRevalidationError(ctx, err)
	}
	if err := authority.security.RevalidateRecoveryEligibilitySecurityTx(
		ctx, tx, binding, observation.binding.security,
	); err != nil {
		return recoveryEligibilityRevalidationError(ctx, err)
	}
	return nil
}

func validRecoveryEligibilityAuthorityBinding(binding RecoveryAuthorityBinding) bool {
	return authorizationOperationMatches(binding.Operation, recoveryEligibilityAuthorizationCategory(binding.Operation),
		recoveryEligibilityAuthorizationEndpoint(binding.Operation)) &&
		binding.Provider != "" && validOpaqueID(binding.PlanID) && validDigest(binding.PlanBindingDigest) &&
		binding.PlanTransitionRevision > 0 && validOpaqueID(binding.RepositoryID) &&
		validOpaqueID(binding.RecoveryPointID) && validOpaqueID(binding.CatalogGenerationID) &&
		validDigest(binding.SelectionDigest) && validDigest(binding.SourceRevisionDigest) &&
		validDigest(binding.ManifestDigest) && binding.SourceRef.PlanID == binding.PlanID &&
		binding.SourceRef.PlanBindingDigest == binding.PlanBindingDigest &&
		binding.SourceRef.RepositoryID == binding.RepositoryID &&
		binding.SourceRef.RecoveryPointID == binding.RecoveryPointID &&
		binding.SourceRef.CatalogGenerationID == binding.CatalogGenerationID &&
		binding.SourceRef.SelectionDigest == binding.SelectionDigest &&
		binding.SourceRef.SourceRevisionDigest == binding.SourceRevisionDigest &&
		binding.SourceRef.ManifestDigest == binding.ManifestDigest &&
		binding.TargetMode.Validate() == nil && binding.TargetNodeID > 0 &&
		strings.TrimSpace(binding.TargetRootID) == binding.TargetRootID && binding.TargetRootID != "" &&
		validDigest(binding.RootLocatorDigest) && validDigest(binding.PathDigest) &&
		binding.TargetBaseRevision != "" && binding.CredentialScopeRevision != "" &&
		binding.RootRevision != "" && binding.FilesystemRevision != "" &&
		binding.CapabilityRevision != "" && binding.ConflictPolicy.Validate() == nil &&
		validDigest(binding.OperationSetDigest) && validDigest(binding.DeleteSetDigest) &&
		validRecoveryEligibilitySecurityDecision(binding.SecurityDecision) && validDigest(binding.SecurityDecisionDigest) &&
		validDigest(binding.SecurityFindingSetDigest) && binding.SecurityPolicyRevision != "" &&
		validOpaqueID(binding.PreflightID) && binding.PreflightRevision != "" &&
		binding.PreflightTargetRevision != "" && binding.PreflightNodeRevision != "" &&
		binding.RequiredBytes >= 0 && binding.RequiredInodes >= 0
}

func validRecoveryEligibilitySecurityDecision(decision SecurityDecisionKind) bool {
	return decision == SecurityDecisionAllowClean || decision == SecurityDecisionBlock ||
		decision == SecurityDecisionAdminOverride
}

func recoveryEligibilityAuthorizationCategory(operation AuthorizationReceiptOperation) AuthorizationReceiptCategory {
	switch operation {
	case AuthorizationReceiptSecurityOverride:
		return AuthorizationReceiptCategorySecurityOverride
	case AuthorizationReceiptWriteAuthorize:
		return AuthorizationReceiptCategoryWrite
	case AuthorizationReceiptDeleteAuthorize:
		return AuthorizationReceiptCategoryExactMirrorDelete
	case AuthorizationReceiptExecute:
		return AuthorizationReceiptCategoryExecute
	default:
		return ""
	}
}

func recoveryEligibilityAuthorizationEndpoint(operation AuthorizationReceiptOperation) string {
	switch operation {
	case AuthorizationReceiptSecurityOverride:
		return recoverySecurityOverrideEndpoint
	case AuthorizationReceiptWriteAuthorize:
		return recoveryWriteAuthorizationEndpoint
	case AuthorizationReceiptDeleteAuthorize:
		return recoveryDeleteAuthorizationEndpoint
	case AuthorizationReceiptExecute:
		return recoveryExecuteEndpoint
	default:
		return ""
	}
}

func validRecoveryEligibilitySource(
	binding RecoveryAuthorityBinding,
	observation RecoveryEligibilitySourceObservation,
	source *RecoverySourceNamespaceObservation,
	now time.Time,
) bool {
	if source == nil || source.observation == nil || source.observation.proof == nil ||
		source.observation.pinned == nil || observation.RepositoryCapabilityRevision <= 0 ||
		observation.CapabilityRevision <= 0 || observation.SourceAccessIdentity == "" ||
		observation.SourceFingerprint == "" || observation.ManagedRootIdentity == "" ||
		observation.RepositoryBindingRevision == "" || observation.ProvenanceRevision == "" || now.IsZero() {
		return false
	}
	proof := source.observation.proof
	return proof.authenticatedNodeIdentity != "" && proof.nodeID > 0 && proof.nodeRevision != "" &&
		proof.credentialRevision != "" && proof.taskRevision != "" && proof.producingTaskID > 0 &&
		proof.repositoryBindingRevision == observation.RepositoryBindingRevision &&
		proof.provenanceRevision == observation.ProvenanceRevision && proof.sourceRef == binding.SourceRef &&
		filepath.IsAbs(proof.canonicalPath) && filepath.Clean(proof.canonicalPath) == proof.canonicalPath &&
		proof.observationRevision != "" && !proof.observedAt.IsZero() &&
		!proof.observedAt.UTC().After(now)
}

func validRecoveryEligibilitySourceNamespaceBinding(binding recoveryEligibilityBinding) bool {
	namespace := binding.sourceNamespace
	if namespace == nil || namespace.observation == nil || namespace.observation.proof == nil ||
		namespace.observation.durable == nil ||
		!validRecoverySourceNamespaceSnapshot(namespace.observation.request, namespace.observation.captured) {
		return false
	}
	proof := namespace.observation.proof
	captured := namespace.observation.captured
	return proof.sourceRef == binding.authority.SourceRef && captured.sourceRef == binding.authority.SourceRef &&
		proof.producingTaskID == captured.producingTaskID && proof.taskRevision == captured.taskRevision &&
		proof.canonicalPath == captured.sourcePath && proof.nodeID == captured.nodeID &&
		proof.nodeRevision == captured.nodeRevision && proof.credentialRevision == captured.credentialRevision &&
		proof.repositoryBindingRevision == binding.source.RepositoryBindingRevision &&
		proof.provenanceRevision == binding.source.ProvenanceRevision &&
		captured.repositoryBindingRevision == binding.source.RepositoryBindingRevision &&
		captured.provenanceRevision == binding.source.ProvenanceRevision &&
		strings.TrimSpace(proof.observationRevision) != "" && !proof.observedAt.IsZero()
}

func validRecoveryEligibilitySecurity(
	binding RecoveryAuthorityBinding,
	observation RecoveryEligibilitySecurityObservation,
	now time.Time,
) bool {
	return observation.Complete && observation.Disposition.valid() &&
		observation.PolicyRevision == binding.SecurityPolicyRevision &&
		observation.FindingSetDigest == binding.SecurityFindingSetDigest &&
		!observation.ObservedAt.IsZero() && !now.IsZero() && !observation.ObservedAt.UTC().After(now)
}

func validRecoveryEligibilityTargetRoot(
	binding RecoveryAuthorityBinding,
	snapshot RecoveryEligibilityTargetRootSnapshot,
) bool {
	return snapshot.NodeID == binding.TargetNodeID && snapshot.RootID == binding.TargetRootID &&
		filepath.IsAbs(snapshot.Locator) && filepath.Clean(snapshot.Locator) == snapshot.Locator &&
		snapshot.LocatorDigest == binding.RootLocatorDigest &&
		snapshot.AuthorityRevision != "" && snapshot.RootObservationRevision != "" &&
		snapshot.Policy.ReserveBytes > 0 && snapshot.Policy.ReserveInodes > 0 &&
		snapshot.Policy.OverlapPolicyBinding != "" && snapshot.NodeRevision == binding.PreflightNodeRevision &&
		snapshot.CredentialRevision == binding.CredentialScopeRevision
}

func validRecoveryEligibilityTarget(
	binding RecoveryAuthorityBinding,
	root RecoveryEligibilityTargetRootSnapshot,
	observation RecoveryEligibilityTargetObservation,
	now time.Time,
) bool {
	return observation.Complete && observation.ReadOnly && observation.AuthenticatedNodeIdentity != "" &&
		filepath.IsAbs(observation.CanonicalRoot) && filepath.Clean(observation.CanonicalRoot) == observation.CanonicalRoot &&
		observation.CanonicalRoot == root.Locator && observation.NodeRevision == root.NodeRevision &&
		observation.CredentialRevision == root.CredentialRevision &&
		observation.RootRevision == binding.RootRevision &&
		observation.RootObservationRevision == root.RootObservationRevision &&
		observation.FilesystemRevision == binding.FilesystemRevision &&
		observation.TargetRevision == binding.PreflightTargetRevision &&
		!observation.ObservedAt.IsZero() && !observation.ExpiresAt.IsZero() &&
		!observation.ObservedAt.UTC().After(now) && now.Before(observation.ExpiresAt.UTC()) &&
		observation.FreeBytes >= 0 && observation.FreeInodes >= 0
}

func recoveryEligibilityCapacityAvailable(available, required, reserved int64) bool {
	return available >= 0 && required >= 0 && reserved > 0 && reserved <= available && required <= available-reserved
}

func recoveryEligibilityBindingDigest(binding recoveryEligibilityBinding) string {
	if !validRecoveryEligibilitySourceNamespaceBinding(binding) {
		return ""
	}
	namespace := binding.sourceNamespace.observation
	proof := namespace.proof
	return framedDigest(
		"xirang/recovery/eligibility-authority/v1",
		string(binding.authority.Operation), string(binding.authority.Provider), binding.authority.PlanID,
		binding.authority.PlanBindingDigest, strconv.FormatUint(binding.authority.PlanTransitionRevision, 10),
		binding.authority.RepositoryID, binding.authority.RecoveryPointID, binding.authority.CatalogGenerationID,
		binding.authority.SelectionDigest, binding.authority.SourceRevisionDigest, binding.authority.ManifestDigest,
		string(binding.authority.TargetMode), strconv.FormatUint(uint64(binding.authority.TargetNodeID), 10),
		binding.authority.TargetRootID, binding.authority.RootLocatorDigest, binding.authority.PathDigest,
		binding.authority.TargetBaseRevision, binding.authority.CredentialScopeRevision,
		binding.authority.RootRevision, binding.authority.FilesystemRevision, binding.authority.CapabilityRevision,
		string(binding.authority.ConflictPolicy), binding.authority.OperationSetDigest, binding.authority.DeleteSetDigest,
		string(binding.authority.SecurityDecision), binding.authority.SecurityDecisionDigest,
		binding.authority.SecurityFindingSetDigest, binding.authority.SecurityPolicyRevision,
		binding.authority.SecurityOverrideBindingDigest, binding.authority.PreflightID,
		binding.authority.PreflightRevision, binding.authority.PreflightTargetRevision,
		binding.authority.PreflightNodeRevision, strconv.FormatInt(binding.authority.RequiredBytes, 10),
		strconv.FormatInt(binding.authority.RequiredInodes, 10),
		strconv.Itoa(binding.source.RepositoryCapabilityRevision), strconv.Itoa(binding.source.CapabilityRevision),
		binding.source.SourceAccessIdentity, binding.source.SourceFingerprint, binding.source.ManagedRootIdentity,
		binding.source.RepositoryBindingRevision, binding.source.ProvenanceRevision,
		proof.authenticatedNodeIdentity, strconv.FormatUint(uint64(proof.nodeID), 10),
		proof.nodeRevision, proof.credentialRevision, proof.taskRevision,
		strconv.FormatUint(uint64(proof.producingTaskID), 10), proof.repositoryBindingRevision,
		proof.provenanceRevision, proof.canonicalPath, proof.observationRevision,
		proof.observedAt.UTC().Format(time.RFC3339Nano),
		binding.security.PolicyRevision, binding.security.FindingSetDigest, string(binding.security.Disposition),
		strconv.FormatBool(binding.security.Complete), binding.security.ObservedAt.UTC().Format(time.RFC3339Nano),
		strconv.FormatUint(uint64(binding.targetRoot.NodeID), 10), binding.targetRoot.RootID,
		binding.targetRoot.LocatorDigest, binding.targetRoot.AuthorityRevision,
		binding.targetRoot.RootObservationRevision, strconv.FormatInt(binding.targetRoot.Policy.ReserveBytes, 10),
		strconv.FormatInt(binding.targetRoot.Policy.ReserveInodes, 10), binding.targetRoot.Policy.OverlapPolicyBinding,
		binding.targetRoot.NodeRevision, binding.targetRoot.CredentialRevision,
		binding.target.AuthenticatedNodeIdentity, binding.target.NodeRevision, binding.target.CredentialRevision,
		binding.target.RootRevision, binding.target.RootObservationRevision, binding.target.FilesystemRevision,
		binding.target.TargetRevision, strconv.FormatInt(binding.target.FreeBytes, 10),
		strconv.FormatInt(binding.target.FreeInodes, 10), binding.target.ObservedAt.UTC().Format(time.RFC3339Nano),
		binding.target.ExpiresAt.UTC().Format(time.RFC3339Nano), strconv.FormatBool(binding.target.ReadOnly),
		strconv.FormatBool(binding.target.Complete), strconv.FormatBool(binding.sourceAccessible),
		strconv.FormatBool(binding.overlapsXirangRoot), strconv.FormatBool(binding.overlapsSourceRoot),
		strconv.FormatInt(binding.reservedBytes, 10), strconv.FormatInt(binding.reservedInodes, 10),
		string(binding.findingDisposition),
	)
}

func recoveryEligibilityObservationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrRecoveryTargetChanged) || errors.Is(err, backupasset.ErrConflict) {
		return ErrRecoveryTargetChanged
	}
	return ErrRecoveryTargetUnavailable
}

func recoveryEligibilityRevalidationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRecoveryTargetChanged
}

func (authority *RecoveryEligibilityAuthority) ObserveRecoveryPreflightEvidence(
	ctx context.Context,
	request PreflightExternalEvidenceRequest,
) (PreflightExternalEvidenceObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PreflightExternalEvidenceObservation{}, err
	}
	if authority == nil || authority.db == nil {
		return PreflightExternalEvidenceObservation{}, ErrTargetPreflightUnavailable
	}
	binding, err := loadRecoveryEligibilityPreflightBinding(ctx, authority.db, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PreflightExternalEvidenceObservation{}, ctxErr
		}
		return PreflightExternalEvidenceObservation{}, ErrTargetPreflightUnavailable
	}
	sealed, err := authority.ObserveRecoveryAuthority(ctx, binding)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PreflightExternalEvidenceObservation{}, ctxErr
		}
		if errors.Is(err, ErrRecoveryTargetChanged) {
			return PreflightExternalEvidenceObservation{}, ErrRecoveryPreflightConflict
		}
		return PreflightExternalEvidenceObservation{}, ErrTargetPreflightUnavailable
	}
	return recoveryEligibilityPreflightProjection(request, sealed)
}

func loadRecoveryEligibilityPreflightBinding(
	ctx context.Context,
	db *gorm.DB,
	request PreflightExternalEvidenceRequest,
) (RecoveryAuthorityBinding, error) {
	if ctx == nil || db == nil || !validOpaqueID(request.PlanID) || !validDigest(request.PlanBindingDigest) ||
		request.PlanTransitionRevision == 0 || !validDigest(request.SourceRevisionDigest) ||
		request.CapabilityRevision == "" || request.PolicyRevision == "" ||
		!validDigest(request.FindingSetDigest) || request.TargetRootRevision == "" ||
		request.TargetFilesystemRevision == "" || request.TargetRevision == "" ||
		request.RequiredBytes < 0 || request.RequiredInodes < 0 {
		return RecoveryAuthorityBinding{}, ErrTargetPreflightUnavailable
	}
	var plan model.BackupAssetRecoveryPlan
	var providerKind string
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded := tx.WithContext(ctx).Where("id = ?", request.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil || loaded.RowsAffected != 1 {
			return ErrTargetPreflightUnavailable
		}
		var repository struct{ ProviderKind string }
		loaded = tx.WithContext(ctx).Table((model.BackupRepository{}).TableName()).
			Select("provider_kind").Where("id = ?", plan.RepositoryID).Limit(1).Find(&repository)
		if loaded.Error != nil || loaded.RowsAffected != 1 {
			return ErrTargetPreflightUnavailable
		}
		providerKind = repository.ProviderKind
		return nil
	})
	if err != nil {
		return RecoveryAuthorityBinding{}, err
	}
	if plan.BindingDigest != request.PlanBindingDigest ||
		plan.TransitionRevision != request.PlanTransitionRevision ||
		plan.SourceRevisionDigest != request.SourceRevisionDigest ||
		plan.CapabilityRevision != request.CapabilityRevision ||
		plan.SecurityPolicyRevision != request.PolicyRevision ||
		plan.SecurityFindingSetDigest != request.FindingSetDigest ||
		plan.RootRevision != request.TargetRootRevision ||
		plan.FilesystemRevision != request.TargetFilesystemRevision ||
		plan.TargetBaseRevision != request.TargetRevision ||
		plan.EstimatedBytes != request.RequiredBytes || plan.EstimatedItems != request.RequiredInodes {
		return RecoveryAuthorityBinding{}, ErrRecoveryPreflightConflict
	}
	binding := RecoveryAuthorityBinding{
		Operation: AuthorizationReceiptWriteAuthorize, Provider: backupasset.ProviderKind(providerKind),
		PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest, PlanTransitionRevision: plan.TransitionRevision,
		RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID,
		CatalogGenerationID: plan.CatalogGenerationID, SelectionDigest: plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest, ManifestDigest: plan.ImmutableManifestDigest,
		TargetMode: TargetMode(plan.TargetMode), TargetNodeID: plan.TargetNodeID, TargetRootID: plan.TargetRootID,
		RootLocatorDigest: plan.RootLocatorDigest, PathDigest: plan.PathDigest,
		TargetBaseRevision: plan.TargetBaseRevision, CredentialScopeRevision: plan.CredentialScopeRevision,
		RootRevision: request.TargetRootRevision, FilesystemRevision: request.TargetFilesystemRevision,
		CapabilityRevision: plan.CapabilityRevision, ConflictPolicy: ConflictPolicy(plan.ConflictPolicy),
		OperationSetDigest: plan.OperationSetDigest, DeleteSetDigest: plan.DeleteSetDigest,
		SecurityDecision:              SecurityDecisionKind(plan.SecurityDecision),
		SecurityDecisionDigest:        plan.SecurityDecisionDigest,
		SecurityFindingSetDigest:      plan.SecurityFindingSetDigest,
		SecurityPolicyRevision:        plan.SecurityPolicyRevision,
		SecurityOverrideBindingDigest: plan.SecurityOverrideBindingDigest,
		// Preflight has not yet been persisted. These private placeholders bind
		// the observation to the exact plan and current target facts without
		// inventing a public or durable preflight identity.
		PreflightID: plan.ID, PreflightRevision: plan.PreflightRevision,
		PreflightTargetRevision: request.TargetRevision, PreflightNodeRevision: plan.TargetBaseRevision,
		RequiredBytes: request.RequiredBytes, RequiredInodes: request.RequiredInodes,
	}
	binding.SourceRef = provider.RsyncRestoreSourceRef{
		PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest, RepositoryID: plan.RepositoryID,
		RecoveryPointID: plan.RecoveryPointID, CatalogGenerationID: plan.CatalogGenerationID,
		SelectionDigest: plan.SelectionDigest, SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest: plan.ImmutableManifestDigest,
	}
	if !validRecoveryEligibilityAuthorityBinding(binding) {
		return RecoveryAuthorityBinding{}, ErrTargetPreflightUnavailable
	}
	return binding, nil
}

func recoveryEligibilityPreflightProjection(
	request PreflightExternalEvidenceRequest,
	sealed RecoveryAuthorityObservation,
) (PreflightExternalEvidenceObservation, error) {
	if sealed.proof == nil || !sealed.proof.production ||
		sealed.proof.bindingDigest != recoveryEligibilityBindingDigest(sealed.binding) ||
		sealed.binding.authority.PlanID != request.PlanID ||
		sealed.binding.authority.PlanBindingDigest != request.PlanBindingDigest ||
		sealed.binding.authority.PlanTransitionRevision != request.PlanTransitionRevision ||
		sealed.binding.authority.SourceRevisionDigest != request.SourceRevisionDigest ||
		sealed.binding.authority.CapabilityRevision != request.CapabilityRevision ||
		sealed.binding.security.PolicyRevision != request.PolicyRevision ||
		sealed.binding.security.FindingSetDigest != request.FindingSetDigest ||
		sealed.binding.target.RootRevision != request.TargetRootRevision ||
		sealed.binding.target.FilesystemRevision != request.TargetFilesystemRevision ||
		sealed.binding.target.TargetRevision != request.TargetRevision ||
		sealed.binding.authority.RequiredBytes != request.RequiredBytes ||
		sealed.binding.authority.RequiredInodes != request.RequiredInodes {
		return PreflightExternalEvidenceObservation{}, ErrRecoveryPreflightConflict
	}
	return PreflightExternalEvidenceObservation{
		PlanID: request.PlanID, PlanBindingDigest: request.PlanBindingDigest,
		PlanTransitionRevision: request.PlanTransitionRevision,
		SourceRevisionDigest:   request.SourceRevisionDigest, CapabilityRevision: request.CapabilityRevision,
		PolicyRevision: request.PolicyRevision, FindingSetDigest: request.FindingSetDigest,
		TargetRootRevision:       request.TargetRootRevision,
		TargetFilesystemRevision: request.TargetFilesystemRevision, TargetRevision: request.TargetRevision,
		RequiredBytes: request.RequiredBytes, RequiredInodes: request.RequiredInodes,
		ObservedAt: sealed.observedAt, ExpiresAt: sealed.expiresAt,
		FindingDisposition: sealed.binding.findingDisposition,
		SourceAccessible:   sealed.binding.sourceAccessible,
		OverlapsXirangRoot: sealed.binding.overlapsXirangRoot,
		OverlapsSourceRoot: sealed.binding.overlapsSourceRoot,
		ReservedBytes:      sealed.binding.reservedBytes, ReservedInodes: sealed.binding.reservedInodes,
	}, nil
}

func (authority *RecoveryEligibilityAuthority) ResolveRecoveryReconciliationRevisionsTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (RecoveryReconciliationRevisionSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryReconciliationRevisionSnapshot{}, err
	}
	if authority == nil || authority.targetRoot == nil || tx == nil || nodeID == 0 ||
		strings.TrimSpace(rootID) == "" || strings.TrimSpace(rootID) != rootID {
		return RecoveryReconciliationRevisionSnapshot{}, ErrRecoveryReconciliationUnavailable
	}
	result, err := authority.targetRoot.ResolveRecoveryReconciliationRevisionsTx(ctx, tx, nodeID, rootID)
	if err != nil || !result.valid() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryReconciliationRevisionSnapshot{}, ctxErr
		}
		return RecoveryReconciliationRevisionSnapshot{}, ErrRecoveryReconciliationUnavailable
	}
	return result, nil
}

func marshalPrivateRecoveryEligibilityProduct() ([]byte, error) {
	return []byte("{}"), nil
}

func (RecoveryEligibilitySourceObservation) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryEligibilitySecurityObservation) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryEligibilityTargetRootSnapshot) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryEligibilityTargetObservationRequest) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryEligibilityTargetObservation) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryAuthorityObservation) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

func (RecoveryAuthorityBinding) MarshalJSON() ([]byte, error) {
	return marshalPrivateRecoveryEligibilityProduct()
}

var _ RecoveryPreflightExternalEvidenceAuthority = (*RecoveryEligibilityAuthority)(nil)
var _ RecoveryReconciliationRevisionSource = (*RecoveryEligibilityAuthority)(nil)
var _ RecoveryAuthorityRevalidator = (*RecoveryEligibilityAuthority)(nil)
var _ fmt.Stringer = RecoveryAuthorityObservation{}
