package recovery

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

// RecoveryApplicationMaterializer is the private authority boundary that turns
// public intent into already-frozen Task 2/3 products. Its production
// implementation must own source freezing, target observation/enumeration,
// security evidence, revisions, estimates, permits, and operation products.
type RecoveryApplicationMaterializer interface {
	MaterializeCreatePlan(context.Context, CreatePlanIntentRequest) (CreatePlanRequest, error)
	MaterializePreflight(context.Context, RecoveryPreflightRequest) (PreflightPersistenceRequest, error)
}

type recoveryApplicationSelectionFreezer interface {
	FreezeSelection(context.Context, SourceSelectionRequest) (ExactSelection, error)
}

// RecoveryPlanSourceItemEvidence is the private, Processing-owned product used
// to derive target operations. Every field is excluded from JSON because the
// locator and content identity are authority material, not API data.
type RecoveryPlanSourceItemEvidence struct {
	AssetRef              backupasset.AssetRef `json:"-"`
	TargetRelativeLocator string               `json:"-"`
	ContentDigest         string               `json:"-"`
	Bytes                 int64                `json:"-"`
	DisplayClass          RecoveryDisplayClass `json:"-"`
}

func (RecoveryPlanSourceItemEvidence) String() string   { return "[recovery source item evidence]" }
func (RecoveryPlanSourceItemEvidence) GoString() string { return "[recovery source item evidence]" }

// RecoveryPlanSecurityRequest is a closed request to the Processing evidence
// authority. The authority must return one exact item for every selected ref.
type RecoveryPlanSecurityRequest struct {
	RequesterID uint           `json:"-"`
	Selection   ExactSelection `json:"-"`
	MaxItems    int            `json:"-"`
	MaxBytes    int64          `json:"-"`
}

func (RecoveryPlanSecurityRequest) String() string   { return "[recovery security request]" }
func (RecoveryPlanSecurityRequest) GoString() string { return "[recovery security request]" }

// RecoveryPlanSecurityEvidence is a sealed pre-create product. It deliberately
// cannot be serialized or formatted with its private per-item evidence.
type RecoveryPlanSecurityEvidence struct {
	SelectionDigest    string                           `json:"-"`
	Provider           backupasset.ProviderKind         `json:"-"`
	CapabilityRevision string                           `json:"-"`
	Security           PreflightSecurityDecision        `json:"-"`
	Items              []RecoveryPlanSourceItemEvidence `json:"-"`
	ObservedAt         time.Time                        `json:"-"`
}

func (RecoveryPlanSecurityEvidence) String() string   { return "[recovery security evidence]" }
func (RecoveryPlanSecurityEvidence) GoString() string { return "[recovery security evidence]" }

// RecoveryPlanSecurityAuthority owns current Processing publication and scan
// evidence. It is invoked both before create and again before preflight.
type RecoveryPlanSecurityAuthority interface {
	ObserveRecoveryPlanSecurity(context.Context, RecoveryPlanSecurityRequest) (RecoveryPlanSecurityEvidence, error)
}

// RecoveryPlanTargetEnumerationRequest is the only input to the bounded,
// read-only target observer. Its private paths and identities never cross HTTP.
type RecoveryPlanTargetEnumerationRequest struct {
	RequesterID     uint                             `json:"-"`
	SelectionDigest string                           `json:"-"`
	TargetMode      TargetMode                       `json:"-"`
	TargetNodeID    uint                             `json:"-"`
	TargetRootID    string                           `json:"-"`
	ConflictPolicy  ConflictPolicy                   `json:"-"`
	Items           []RecoveryPlanSourceItemEvidence `json:"-"`
	MaxRows         int                              `json:"-"`
	MaxBytes        int64                            `json:"-"`
	ExpiresAt       time.Time                        `json:"-"`
}

func (RecoveryPlanTargetEnumerationRequest) String() string {
	return "[recovery target enumeration request]"
}
func (RecoveryPlanTargetEnumerationRequest) GoString() string {
	return "[recovery target enumeration request]"
}

// RecoveryPlanTargetEnumeration is the private immutable result of a complete
// target snapshot. Implementations must fail rather than return truncation.
type RecoveryPlanTargetEnumeration struct {
	Target         TargetBinding                  `json:"-"`
	TargetRevision string                         `json:"-"`
	Node           RecoveryPlanTargetNodeEvidence `json:"-"`
	Operations     RecoveryOperationProducts      `json:"-"`
}

func (RecoveryPlanTargetEnumeration) String() string {
	return "[recovery target enumeration]"
}
func (RecoveryPlanTargetEnumeration) GoString() string {
	return "[recovery target enumeration]"
}

type RecoveryPlanTargetEnumerationAuthority interface {
	EnumerateRecoveryPlanTarget(context.Context, RecoveryPlanTargetEnumerationRequest) (RecoveryPlanTargetEnumeration, error)
}

// RecoveryPlanTargetNodeEvidence is a closed projection from the strict
// session admission. It contains no endpoint, credential, or identity proof.
type RecoveryPlanTargetNodeEvidence struct {
	Registered bool `json:"-"`
	Archived   bool `json:"-"`
	Online     bool `json:"-"`
	Authorized bool `json:"-"`
	Producing  bool `json:"-"`
}

// RecoveryApplicationMaterializationPolicy bounds every pre-create read. A
// complete result is required; reaching a limit is a closed failure.
type RecoveryApplicationMaterializationPolicy struct {
	MaxSelectionItems  int
	MaxLogicalBytes    int64
	MaxTargetRows      int
	MaxTargetBytes     int64
	ObservationTimeout time.Duration
	PreflightTTL       time.Duration
}

func (policy RecoveryApplicationMaterializationPolicy) valid() bool {
	return policy.MaxSelectionItems > 0 && policy.MaxSelectionItems <= exactSelectionMaxItems &&
		policy.MaxLogicalBytes >= 0 && policy.MaxTargetRows > 0 &&
		policy.MaxTargetRows <= exactSelectionMaxItems && policy.MaxTargetBytes >= 0 &&
		policy.ObservationTimeout > 0 && policy.PreflightTTL > 0
}

type ProductionApplicationMaterializerDependencies struct {
	Selections  recoveryApplicationSelectionFreezer
	Plans       RecoveryApplicationPlanAuthority
	Security    RecoveryPlanSecurityAuthority
	Targets     RecoveryPlanTargetEnumerationAuthority
	Policy      RecoveryApplicationMaterializationPolicy
	Now         func() time.Time
	NewRevision func() (string, error)
	NewID       func() (string, error)
}

// ProductionApplicationMaterializer owns the server-only composition from
// safe public intent into immutable Recovery products.
type ProductionApplicationMaterializer struct {
	selections  recoveryApplicationSelectionFreezer
	plans       RecoveryApplicationPlanAuthority
	security    RecoveryPlanSecurityAuthority
	targets     RecoveryPlanTargetEnumerationAuthority
	policy      RecoveryApplicationMaterializationPolicy
	now         func() time.Time
	newRevision func() (string, error)
	newID       func() (string, error)
}

func NewProductionApplicationMaterializer(
	dependencies ProductionApplicationMaterializerDependencies,
) (*ProductionApplicationMaterializer, error) {
	if dependencies.Selections == nil || dependencies.Plans == nil || dependencies.Security == nil || dependencies.Targets == nil ||
		!dependencies.Policy.valid() || dependencies.Now == nil {
		return nil, ErrRecoveryPlanUnavailable
	}
	if dependencies.NewRevision == nil {
		dependencies.NewRevision = newRecoveryApplicationRevision
	}
	if dependencies.NewID == nil {
		dependencies.NewID = newRecoveryApplicationID
	}
	return &ProductionApplicationMaterializer{
		selections: dependencies.Selections, plans: dependencies.Plans,
		security: dependencies.Security, targets: dependencies.Targets,
		policy: dependencies.Policy, now: dependencies.Now,
		newRevision: dependencies.NewRevision, newID: dependencies.NewID,
	}, nil
}

func newRecoveryApplicationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrRecoveryPlanUnavailable
	}
	return hex.EncodeToString(raw[:]), nil
}

func newRecoveryApplicationRevision() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrRecoveryPlanUnavailable
	}
	return "recovery-revision-" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (materializer *ProductionApplicationMaterializer) MaterializeCreatePlan(
	ctx context.Context,
	intent CreatePlanIntentRequest,
) (CreatePlanRequest, error) {
	if materializer == nil || materializer.selections == nil || materializer.security == nil ||
		materializer.targets == nil || validateCreatePlanIntent(intent) != nil {
		return CreatePlanRequest{}, ErrInvalidRecoveryPlan
	}
	now := materializer.now().UTC()
	if now.IsZero() {
		return CreatePlanRequest{}, ErrRecoveryPlanUnavailable
	}
	refs := make([]backupasset.AssetRef, len(intent.EntryIDs))
	for index, entryID := range intent.EntryIDs {
		refs[index] = backupasset.AssetRef{RecoveryPointID: intent.RecoveryPointID, EntryID: entryID}
	}
	selection, err := materializer.selections.FreezeSelection(nonNilRecoveryAPIContext(ctx), SourceSelectionRequest{
		RepositoryID: intent.RepositoryID, RecoveryPointID: intent.RecoveryPointID,
		CatalogGenerationID: intent.CatalogGenerationID, AssetRefs: refs,
		MaxItems: materializer.policy.MaxSelectionItems,
	})
	if err != nil {
		return CreatePlanRequest{}, err
	}
	if selection.Validate() != nil || selection.RepositoryID != intent.RepositoryID ||
		selection.RecoveryPointID != intent.RecoveryPointID ||
		selection.CatalogGenerationID != intent.CatalogGenerationID {
		return CreatePlanRequest{}, ErrRecoverySourceChanged
	}

	observeCtx, cancel := context.WithTimeout(nonNilRecoveryAPIContext(ctx), materializer.policy.ObservationTimeout)
	defer cancel()
	security, err := materializer.security.ObserveRecoveryPlanSecurity(observeCtx, RecoveryPlanSecurityRequest{
		RequesterID: intent.RequesterID, Selection: selection,
		MaxItems: materializer.policy.MaxSelectionItems, MaxBytes: materializer.policy.MaxLogicalBytes,
	})
	if err != nil {
		return CreatePlanRequest{}, err
	}
	securityValidatedAt := materializer.now().UTC()
	if securityValidatedAt.IsZero() || securityValidatedAt.Before(now) {
		return CreatePlanRequest{}, ErrRecoveryPlanUnavailable
	}
	if err := validateRecoveryPlanSecurityEvidence(selection, security, securityValidatedAt, materializer.policy); err != nil {
		return CreatePlanRequest{}, err
	}

	expiresAt := now.Add(materializer.policy.PreflightTTL)
	target, err := materializer.targets.EnumerateRecoveryPlanTarget(observeCtx, RecoveryPlanTargetEnumerationRequest{
		RequesterID: intent.RequesterID, SelectionDigest: selection.SelectionDigest,
		TargetMode: intent.TargetMode, TargetNodeID: intent.TargetNodeID, TargetRootID: intent.TargetRootID,
		ConflictPolicy: intent.ConflictPolicy, Items: cloneRecoveryPlanSourceItemEvidence(security.Items),
		MaxRows: materializer.policy.MaxTargetRows, MaxBytes: materializer.policy.MaxTargetBytes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return CreatePlanRequest{}, err
	}
	products, err := validateRecoveryPlanTargetEnumeration(intent, selection, security, target, materializer.policy)
	if err != nil {
		return CreatePlanRequest{}, err
	}
	revision, err := materializer.newRevision()
	if err != nil || !validOpaqueRevision(revision) || sha256Shaped(revision) {
		return CreatePlanRequest{}, ErrRecoveryPlanUnavailable
	}
	request := CreatePlanRequest{
		RequesterID: intent.RequesterID, Endpoint: intent.Endpoint, IdempotencyKey: intent.IdempotencyKey,
		Selection: selection, AuthorityCategory: AuthorityWrite,
		EstimatedItems: products.Impact.EstimatedItems, EstimatedBytes: products.Impact.EstimatedBytes,
		Plan: RecoveryPlan{
			PreflightExpiresAt: expiresAt,
			Binding: PlanBinding{
				SchemaVersion: 1, PlanDigest: strings.Repeat("0", sha256DigestLength),
				SelectionDigest: selection.SelectionDigest, RepositoryID: selection.RepositoryID,
				RecoveryPointID: selection.RecoveryPointID, SourceRevisionDigest: selection.SourceRevisionDigest,
				SourceRevision: cloneSourceRevision(selection.SourceRevision), Target: target.Target,
				ConflictPolicy: intent.ConflictPolicy, OperationSetDigest: products.OperationSetDigest,
				DeleteSetDigest: products.DeleteSetDigest, CapabilityRevision: security.CapabilityRevision,
				SecurityDecision: security.Security.Decision, PreflightRevision: revision,
			},
		},
	}
	if request.Plan.ValidateAt(now) != nil {
		return CreatePlanRequest{}, ErrRecoveryPlanUnavailable
	}
	return request, nil
}

func validateRecoveryPlanSecurityEvidence(
	selection ExactSelection,
	evidence RecoveryPlanSecurityEvidence,
	now time.Time,
	policy RecoveryApplicationMaterializationPolicy,
) error {
	if evidence.SelectionDigest != selection.SelectionDigest || !validRecoveryProvider(evidence.Provider) ||
		!validOpaqueRevision(evidence.CapabilityRevision) || evidence.Security.Decision.Validate() != nil ||
		evidence.ObservedAt.IsZero() || evidence.ObservedAt.After(now) || len(evidence.Items) != len(selection.AssetRefs) ||
		len(evidence.Items) > policy.MaxSelectionItems {
		return ErrRecoveryPlanUnavailable
	}
	byRef := make(map[backupasset.AssetRef]RecoveryPlanSourceItemEvidence, len(evidence.Items))
	var totalBytes int64
	for _, item := range evidence.Items {
		if backupasset.ValidateAssetRef(item.AssetRef) != nil || item.AssetRef.RecoveryPointID != selection.RecoveryPointID ||
			!validTargetRelativeLocator(item.TargetRelativeLocator) || !validDigest(item.ContentDigest) ||
			item.Bytes < 0 || !item.DisplayClass.valid() {
			return ErrRecoveryPlanUnavailable
		}
		if _, duplicate := byRef[item.AssetRef]; duplicate {
			return ErrRecoveryPlanUnavailable
		}
		if item.Bytes > policy.MaxLogicalBytes-totalBytes {
			return ErrExactSelectionLimit
		}
		totalBytes += item.Bytes
		byRef[item.AssetRef] = item
	}
	for _, ref := range selection.AssetRefs {
		if _, exists := byRef[ref]; !exists {
			return ErrRecoverySourceChanged
		}
	}
	return nil
}

func validateRecoveryPlanTargetEnumeration(
	intent CreatePlanIntentRequest,
	selection ExactSelection,
	security RecoveryPlanSecurityEvidence,
	enumeration RecoveryPlanTargetEnumeration,
	policy RecoveryApplicationMaterializationPolicy,
) (RecoveryOperationProducts, error) {
	if enumeration.Target.Validate() != nil || enumeration.Target.Mode != intent.TargetMode ||
		enumeration.Target.NodeID != intent.TargetNodeID || enumeration.Target.RootID != intent.TargetRootID {
		return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
	}
	rebuilt, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: intent.TargetMode, ConflictPolicy: intent.ConflictPolicy,
		Operations: enumeration.Operations.Rows,
		Limits: RecoveryOperationLimits{
			MaxRows: policy.MaxTargetRows, MaxItems: policy.MaxTargetRows,
			MaxBytes: policy.MaxTargetBytes, MaxImpactRows: policy.MaxTargetRows,
		},
	})
	if err != nil {
		return RecoveryOperationProducts{}, err
	}
	if rebuilt.OperationSetDigest != enumeration.Operations.OperationSetDigest ||
		rebuilt.DeleteSetDigest != enumeration.Operations.DeleteSetDigest ||
		rebuilt.Impact.EstimatedItems != enumeration.Operations.Impact.EstimatedItems ||
		rebuilt.Impact.EstimatedBytes != enumeration.Operations.Impact.EstimatedBytes {
		return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
	}
	wanted := make(map[backupasset.AssetRef]RecoveryPlanSourceItemEvidence, len(security.Items))
	for _, item := range security.Items {
		wanted[item.AssetRef] = item
	}
	seen := make(map[backupasset.AssetRef]struct{}, len(wanted))
	for _, operation := range rebuilt.Rows {
		if operation.Kind == RecoveryOperationDelete {
			continue
		}
		if operation.Source.AssetRef == nil {
			return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
		}
		item, exists := wanted[*operation.Source.AssetRef]
		if !exists || operation.TargetRelativeLocator != item.TargetRelativeLocator ||
			operation.EstimatedBytes != item.Bytes {
			return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
		}
		seen[*operation.Source.AssetRef] = struct{}{}
	}
	if len(seen) != len(selection.AssetRefs) {
		return RecoveryOperationProducts{}, ErrRecoveryTargetChanged
	}
	return rebuilt, nil
}

func cloneRecoveryPlanSourceItemEvidence(items []RecoveryPlanSourceItemEvidence) []RecoveryPlanSourceItemEvidence {
	return append([]RecoveryPlanSourceItemEvidence(nil), items...)
}

func (materializer *ProductionApplicationMaterializer) MaterializePreflight(
	ctx context.Context,
	request RecoveryPreflightRequest,
) (PreflightPersistenceRequest, error) {
	if materializer == nil || materializer.plans == nil || materializer.security == nil || materializer.targets == nil ||
		request.RequesterID == 0 || !validOpaqueID(request.PlanID) || request.ExpectedPlanRevision == 0 {
		return PreflightPersistenceRequest{}, ErrInvalidTargetPreflight
	}
	now := materializer.now().UTC()
	if now.IsZero() {
		return PreflightPersistenceRequest{}, ErrTargetPreflightUnavailable
	}
	plan, err := materializer.plans.LoadRecoveryApplicationPlan(nonNilRecoveryAPIContext(ctx), RecoveryApplicationPlanRequest{
		RequesterID: request.RequesterID, PlanID: request.PlanID,
		ExpectedRevision: request.ExpectedPlanRevision, ObservedAt: now,
	})
	if err != nil {
		return PreflightPersistenceRequest{}, err
	}
	if plan.PlanID != request.PlanID || plan.RequesterID != request.RequesterID ||
		plan.TransitionRevision != request.ExpectedPlanRevision || plan.Selection.Validate() != nil ||
		!plan.PreflightExpiresAt.After(now) || plan.PreflightExpiresAt.Sub(now) > materializer.policy.PreflightTTL {
		return PreflightPersistenceRequest{}, ErrRecoveryPreflightConflict
	}
	observeCtx, cancel := context.WithTimeout(nonNilRecoveryAPIContext(ctx), materializer.policy.ObservationTimeout)
	defer cancel()
	security, err := materializer.security.ObserveRecoveryPlanSecurity(observeCtx, RecoveryPlanSecurityRequest{
		RequesterID: request.RequesterID, Selection: plan.Selection,
		MaxItems: materializer.policy.MaxSelectionItems, MaxBytes: materializer.policy.MaxLogicalBytes,
	})
	if err != nil {
		return PreflightPersistenceRequest{}, err
	}
	securityValidatedAt := materializer.now().UTC()
	if securityValidatedAt.IsZero() || securityValidatedAt.Before(now) {
		return PreflightPersistenceRequest{}, ErrTargetPreflightUnavailable
	}
	if err := validateRecoveryPlanSecurityEvidence(plan.Selection, security, securityValidatedAt, materializer.policy); err != nil {
		return PreflightPersistenceRequest{}, err
	}
	if security.CapabilityRevision != plan.CapabilityRevision || security.Security.Decision != plan.SecurityDecision {
		return PreflightPersistenceRequest{}, ErrRecoverySourceChanged
	}
	target, err := materializer.targets.EnumerateRecoveryPlanTarget(observeCtx, RecoveryPlanTargetEnumerationRequest{
		RequesterID: request.RequesterID, SelectionDigest: plan.Selection.SelectionDigest,
		TargetMode: plan.Target.Mode, TargetNodeID: plan.Target.NodeID, TargetRootID: plan.Target.RootID,
		ConflictPolicy: plan.ConflictPolicy, Items: cloneRecoveryPlanSourceItemEvidence(security.Items),
		MaxRows: materializer.policy.MaxTargetRows, MaxBytes: materializer.policy.MaxTargetBytes,
		ExpiresAt: plan.PreflightExpiresAt,
	})
	if err != nil {
		return PreflightPersistenceRequest{}, err
	}
	products, err := validateRecoveryPlanTargetEnumeration(CreatePlanIntentRequest{
		TargetMode: plan.Target.Mode, TargetNodeID: plan.Target.NodeID,
		TargetRootID: plan.Target.RootID, ConflictPolicy: plan.ConflictPolicy,
	}, plan.Selection, security, target, materializer.policy)
	if err != nil {
		return PreflightPersistenceRequest{}, err
	}
	if target.Target != plan.Target || products.OperationSetDigest != plan.OperationSetDigest ||
		products.DeleteSetDigest != plan.DeleteSetDigest || products.Impact.EstimatedItems != plan.EstimatedItems ||
		products.Impact.EstimatedBytes != plan.EstimatedBytes || !validOpaqueRevision(target.TargetRevision) ||
		!target.Node.Registered || target.Node.Archived || !target.Node.Online || !target.Node.Authorized {
		return PreflightPersistenceRequest{}, ErrRecoveryTargetChanged
	}
	snapshotID, err := materializer.newID()
	if err != nil || !validOpaqueID(snapshotID) {
		return PreflightPersistenceRequest{}, ErrTargetPreflightUnavailable
	}
	permit := TargetObservationPermit{
		SchemaVersion: 1, NodeID: plan.Target.NodeID, Purpose: TargetPurposePreflight,
		RootID: plan.Target.RootID, RootLocatorDigest: plan.Target.RootLocatorDigest,
		TargetPathDigest: plan.Target.PathDigest, RootRevision: plan.Target.RootRevision,
		ExpiresAt: plan.PreflightExpiresAt,
	}
	input := TargetPreflightInput{
		SnapshotID: snapshotID, SnapshotRevision: plan.PreflightRevision,
		SnapshotTTL: plan.PreflightExpiresAt.Sub(now),
		Node: NodeEligibilityFacts{
			NodeID: plan.Target.NodeID, Registered: target.Node.Registered, Archived: target.Node.Archived,
			Online: target.Node.Online, Authorized: target.Node.Authorized, ProducingNode: target.Node.Producing,
			CredentialPurpose: TargetPurposePreflight, NodeRevision: plan.Target.BaseNodeRevision,
			ActiveWriter: plan.ActiveWriter,
		},
		Permit: permit,
		ProbeRequest: TargetProbeRequest{
			Object: TargetObjectRef{
				RootID: plan.Target.RootID, RootLocatorDigest: plan.Target.RootLocatorDigest,
				TargetPathDigest:       plan.Target.PathDigest,
				PrivateRelativeLocator: plan.Target.EncryptedRelativePath,
			},
			SourceRevisionDigest: plan.Selection.SourceRevisionDigest,
			CapabilityRevision:   security.CapabilityRevision, PolicyRevision: security.Security.Decision.PolicyRevision,
			RequiredBytes: products.Impact.EstimatedBytes, RequiredInodes: products.Impact.EstimatedItems,
		},
		Frozen: FrozenPreflightRevisions{
			NodeRevision: plan.Target.BaseNodeRevision, SourceRevisionDigest: plan.Selection.SourceRevisionDigest,
			TargetRevision: target.TargetRevision, CapabilityRevision: security.CapabilityRevision,
			PolicyRevision:   security.Security.Decision.PolicyRevision,
			FindingSetDigest: security.Security.Decision.FindingSetDigest,
		},
		TargetMode: plan.Target.Mode, ConflictPolicy: plan.ConflictPolicy,
		Operations: products, Security: security.Security,
	}
	return PreflightPersistenceRequest{
		RequesterID: request.RequesterID, PlanID: request.PlanID,
		ExpectedPlanRevision: request.ExpectedPlanRevision, Input: input,
	}, nil
}

var _ RecoveryApplicationMaterializer = (*ProductionApplicationMaterializer)(nil)

type recoveryApplicationPlanOwner interface {
	CreatePlan(context.Context, CreatePlanRequest) (CreatePlanResult, error)
}

type recoveryApplicationPreflightOwner interface {
	EvaluateAndPersist(context.Context, PreflightPersistenceRequest) (PreflightPersistenceResult, error)
}

type RecoveryApplicationServiceDependencies struct {
	Materializer RecoveryApplicationMaterializer
	Plans        recoveryApplicationPlanOwner
	Preflights   recoveryApplicationPreflightOwner
}

// ApplicationService is the only handler-facing plan/preflight owner. HTTP
// never supplies ExactSelection, RecoveryPlan, TargetPreflightInput, permits,
// revisions, digests, estimates, or security/operation products.
type ApplicationService struct {
	materializer RecoveryApplicationMaterializer
	plans        recoveryApplicationPlanOwner
	preflights   recoveryApplicationPreflightOwner
}

func NewApplicationService(dependencies RecoveryApplicationServiceDependencies) (*ApplicationService, error) {
	if dependencies.Materializer == nil || dependencies.Plans == nil || dependencies.Preflights == nil {
		return nil, ErrRecoveryAPIUnavailable
	}
	return &ApplicationService{
		materializer: dependencies.Materializer,
		plans:        dependencies.Plans,
		preflights:   dependencies.Preflights,
	}, nil
}

func (service *ApplicationService) CreatePlan(
	ctx context.Context,
	intent CreatePlanIntentRequest,
) (CreatePlanResult, error) {
	if service == nil || service.materializer == nil || service.plans == nil ||
		validateCreatePlanIntent(intent) != nil {
		return CreatePlanResult{}, ErrInvalidRecoveryPlan
	}
	request, err := service.materializer.MaterializeCreatePlan(nonNilRecoveryAPIContext(ctx), intent)
	if err != nil {
		return CreatePlanResult{}, err
	}
	if request.RequesterID != intent.RequesterID || request.Endpoint != intent.Endpoint ||
		request.IdempotencyKey != intent.IdempotencyKey || request.Selection.RepositoryID != intent.RepositoryID ||
		request.Selection.RecoveryPointID != intent.RecoveryPointID ||
		request.Selection.CatalogGenerationID != intent.CatalogGenerationID ||
		request.Plan.Binding.Target.Mode != intent.TargetMode ||
		request.Plan.Binding.Target.NodeID != intent.TargetNodeID ||
		request.Plan.Binding.Target.RootID != intent.TargetRootID ||
		request.Plan.Binding.ConflictPolicy != intent.ConflictPolicy {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	return service.plans.CreatePlan(nonNilRecoveryAPIContext(ctx), request)
}

func (service *ApplicationService) Preflight(
	ctx context.Context,
	request RecoveryPreflightRequest,
) (RecoveryPreflightView, error) {
	if service == nil || service.materializer == nil || service.preflights == nil ||
		request.RequesterID == 0 || !validOpaqueID(request.PlanID) || request.ExpectedPlanRevision == 0 {
		return RecoveryPreflightView{}, ErrInvalidTargetPreflight
	}
	privateRequest, err := service.materializer.MaterializePreflight(nonNilRecoveryAPIContext(ctx), request)
	if err != nil {
		return RecoveryPreflightView{}, err
	}
	if privateRequest.RequesterID != request.RequesterID || privateRequest.PlanID != request.PlanID ||
		privateRequest.ExpectedPlanRevision != request.ExpectedPlanRevision {
		return RecoveryPreflightView{}, ErrTargetPreflightUnavailable
	}
	result, err := service.preflights.EvaluateAndPersist(nonNilRecoveryAPIContext(ctx), privateRequest)
	if err != nil {
		return RecoveryPreflightView{}, err
	}
	return ProjectPreflightResult(result)
}

func validateCreatePlanIntent(intent CreatePlanIntentRequest) error {
	if intent.RequesterID == 0 || intent.Endpoint != "/api/v1/recovery-plans" ||
		len(intent.IdempotencyKey) < 16 || len(intent.IdempotencyKey) > 256 ||
		strings.TrimSpace(intent.IdempotencyKey) != intent.IdempotencyKey ||
		backupasset.ValidateOpaqueID(intent.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(intent.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(intent.CatalogGenerationID) != nil ||
		intent.TargetMode.Validate() != nil || intent.TargetNodeID == 0 ||
		!validBoundedOpaque(intent.TargetRootID, targetRootIDMax) || intent.ConflictPolicy.Validate() != nil ||
		len(intent.EntryIDs) == 0 || len(intent.EntryIDs) > exactSelectionMaxItems ||
		(intent.ConflictPolicy == ConflictExactMirror && intent.TargetMode != TargetModeInPlace) {
		return ErrInvalidRecoveryPlan
	}
	seen := make(map[string]struct{}, len(intent.EntryIDs))
	for _, entryID := range intent.EntryIDs {
		if backupasset.ValidateAssetRef(backupasset.AssetRef{
			RecoveryPointID: intent.RecoveryPointID, EntryID: entryID,
		}) != nil {
			return ErrInvalidRecoveryPlan
		}
		if _, duplicate := seen[entryID]; duplicate {
			return ErrInvalidRecoveryPlan
		}
		seen[entryID] = struct{}{}
	}
	return nil
}

// UnavailableApplicationMaterializer is the explicit fail-closed production
// placeholder used until the approved pre-create target enumeration and
// Processing security materialization authority exists. It never fabricates
// immutable revisions or digests and never submits an empty preflight input.
type UnavailableApplicationMaterializer struct{}

func (UnavailableApplicationMaterializer) MaterializeCreatePlan(
	context.Context,
	CreatePlanIntentRequest,
) (CreatePlanRequest, error) {
	return CreatePlanRequest{}, ErrRecoveryPlanUnavailable
}

func (UnavailableApplicationMaterializer) MaterializePreflight(
	context.Context,
	RecoveryPreflightRequest,
) (PreflightPersistenceRequest, error) {
	return PreflightPersistenceRequest{}, ErrTargetPreflightUnavailable
}

var _ RecoveryApplicationMaterializer = UnavailableApplicationMaterializer{}
