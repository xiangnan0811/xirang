package recovery

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	preflightExternalEvidenceRequestDomain = "xirang/recovery/preflight-external-evidence-request/v1"
	preflightExternalEvidenceProofDomain   = "xirang/recovery/preflight-external-evidence-proof/v1"
)

var (
	ErrInvalidTargetPreflight     = errors.New("invalid recovery target preflight")
	ErrTargetPreflightUnavailable = errors.New("recovery target preflight is unavailable")
)

type TargetPreflightReason string

const (
	TargetPreflightNodeUnregistered   TargetPreflightReason = "node_unregistered"
	TargetPreflightNodeArchived       TargetPreflightReason = "node_archived"
	TargetPreflightNodeOffline        TargetPreflightReason = "node_offline"
	TargetPreflightNodeUnauthorized   TargetPreflightReason = "node_unauthorized"
	TargetPreflightCredentialPurpose  TargetPreflightReason = "credential_purpose_invalid"
	TargetPreflightToolUnavailable    TargetPreflightReason = "tool_unavailable"
	TargetPreflightSourceUnavailable  TargetPreflightReason = "source_unavailable"
	TargetPreflightRootNotReal        TargetPreflightReason = "root_not_real"
	TargetPreflightRootNoncanonical   TargetPreflightReason = "root_noncanonical"
	TargetPreflightDeviceInvalid      TargetPreflightReason = "device_invalid"
	TargetPreflightMountInvalid       TargetPreflightReason = "mount_invalid"
	TargetPreflightOwnerInvalid       TargetPreflightReason = "owner_invalid"
	TargetPreflightModeInvalid        TargetPreflightReason = "mode_invalid"
	TargetPreflightSymlinkComponent   TargetPreflightReason = "symlink_component"
	TargetPreflightXirangOverlap      TargetPreflightReason = "xirang_root_overlap"
	TargetPreflightSourceOverlap      TargetPreflightReason = "source_root_overlap"
	TargetPreflightInsufficientBytes  TargetPreflightReason = "insufficient_bytes"
	TargetPreflightInsufficientInodes TargetPreflightReason = "insufficient_inodes"
	TargetPreflightActiveWriter       TargetPreflightReason = "active_writer"
	TargetPreflightTargetConflict     TargetPreflightReason = "target_conflict"
	TargetPreflightSecurityBlocked    TargetPreflightReason = "security_blocked"
)

type NodeEligibilityFacts struct {
	NodeID            uint
	Registered        bool
	Archived          bool
	Online            bool
	Authorized        bool
	ProducingNode     bool
	CredentialPurpose TargetPurpose
	NodeRevision      string
	ActiveWriter      bool
}

type FrozenPreflightRevisions struct {
	NodeRevision         string
	SourceRevisionDigest string
	TargetRevision       string
	CapabilityRevision   string
	PolicyRevision       string
	FindingSetDigest     string
}

type TargetPreflightInput struct {
	SnapshotID       string
	SnapshotRevision string
	SnapshotTTL      time.Duration
	Node             NodeEligibilityFacts
	Permit           TargetObservationPermit
	ProbeRequest     TargetProbeRequest
	Frozen           FrozenPreflightRevisions
	TargetMode       TargetMode
	ConflictPolicy   ConflictPolicy
	Operations       RecoveryOperationProducts
	Security         PreflightSecurityDecision
	targetPermit     TargetPreflightPermit
}

type PreflightExternalEvidenceRequest struct {
	PlanID                   string
	PlanBindingDigest        string
	PlanTransitionRevision   uint64
	SourceRevisionDigest     string
	CapabilityRevision       string
	PolicyRevision           string
	FindingSetDigest         string
	TargetRootRevision       string
	TargetFilesystemRevision string
	TargetRevision           string
	RequiredBytes            int64
	RequiredInodes           int64
}

type PreflightExternalEvidence struct {
	ObservedAt           time.Time
	ExpiresAt            time.Time
	SourceRevisionDigest string
	CapabilityRevision   string
	PolicyRevision       string
	FindingSetDigest     string
	FindingDisposition   SecurityFindingDisposition
	SourceAccessible     bool
	OverlapsXirangRoot   bool
	OverlapsSourceRoot   bool
	ReservedBytes        int64
	ReservedInodes       int64
	proof                *preflightExternalEvidenceProof
}

type preflightExternalEvidenceProof struct {
	requestDigest string
	bindingDigest string
	production    bool
}

type PreflightExternalEvidencePort interface {
	ObservePreflightEvidence(
		context.Context,
		PreflightExternalEvidenceRequest,
	) (PreflightExternalEvidence, error)
}

// PreflightExternalEvidenceObservation is the closed scalar product returned
// by the independently composed Provider/Repository authority. It deliberately
// carries no locator, credential, command, or target-session material.
type PreflightExternalEvidenceObservation struct {
	PlanID                   string
	PlanBindingDigest        string
	PlanTransitionRevision   uint64
	SourceRevisionDigest     string
	CapabilityRevision       string
	PolicyRevision           string
	FindingSetDigest         string
	TargetRootRevision       string
	TargetFilesystemRevision string
	TargetRevision           string
	RequiredBytes            int64
	RequiredInodes           int64
	ObservedAt               time.Time
	ExpiresAt                time.Time
	FindingDisposition       SecurityFindingDisposition
	SourceAccessible         bool
	OverlapsXirangRoot       bool
	OverlapsSourceRoot       bool
	ReservedBytes            int64
	ReservedInodes           int64
}

// RecoveryPreflightExternalEvidenceAuthority is implemented by the runtime's
// Provider/Repository composition. Recovery supplies only the already sealed
// scalar request and independently validates the returned observation.
type RecoveryPreflightExternalEvidenceAuthority interface {
	ObserveRecoveryPreflightEvidence(
		context.Context,
		PreflightExternalEvidenceRequest,
	) (PreflightExternalEvidenceObservation, error)
}

// RecoveryPreflightExternalEvidenceAdapter is the only production issuer for
// external preflight evidence. Task 8 owns wiring its authority dependency.
type RecoveryPreflightExternalEvidenceAdapter struct {
	authority RecoveryPreflightExternalEvidenceAuthority
}

func NewRecoveryPreflightExternalEvidenceAdapter(
	authority RecoveryPreflightExternalEvidenceAuthority,
) (*RecoveryPreflightExternalEvidenceAdapter, error) {
	if authority == nil {
		return nil, ErrTargetPreflightUnavailable
	}
	return &RecoveryPreflightExternalEvidenceAdapter{authority: authority}, nil
}

func (adapter *RecoveryPreflightExternalEvidenceAdapter) ObservePreflightEvidence(
	ctx context.Context,
	request PreflightExternalEvidenceRequest,
) (PreflightExternalEvidence, error) {
	if adapter == nil || adapter.authority == nil || ctx == nil {
		return PreflightExternalEvidence{}, ErrTargetPreflightUnavailable
	}
	if !request.valid() {
		return PreflightExternalEvidence{}, ErrInvalidTargetPreflight
	}
	if err := ctx.Err(); err != nil {
		return PreflightExternalEvidence{}, err
	}
	observation, err := adapter.authority.ObserveRecoveryPreflightEvidence(ctx, request)
	if err != nil {
		return PreflightExternalEvidence{}, sanitizePreflightExternalEvidenceError(ctx, err)
	}
	if !observation.matches(request) {
		return PreflightExternalEvidence{}, ErrRecoveryPreflightConflict
	}
	evidence := PreflightExternalEvidence{
		ObservedAt: observation.ObservedAt, ExpiresAt: observation.ExpiresAt,
		SourceRevisionDigest: observation.SourceRevisionDigest,
		CapabilityRevision:   observation.CapabilityRevision, PolicyRevision: observation.PolicyRevision,
		FindingSetDigest: observation.FindingSetDigest, FindingDisposition: observation.FindingDisposition,
		SourceAccessible: observation.SourceAccessible, OverlapsXirangRoot: observation.OverlapsXirangRoot,
		OverlapsSourceRoot: observation.OverlapsSourceRoot, ReservedBytes: observation.ReservedBytes,
		ReservedInodes: observation.ReservedInodes,
	}
	if !evidence.validShape() {
		return PreflightExternalEvidence{}, ErrInvalidTargetPreflight
	}
	evidence.proof = &preflightExternalEvidenceProof{
		requestDigest: preflightExternalEvidenceRequestDigest(request),
		production:    true,
	}
	evidence.proof.bindingDigest = preflightExternalEvidenceProofDigest(evidence)
	return evidence, nil
}

func (observation PreflightExternalEvidenceObservation) matches(
	request PreflightExternalEvidenceRequest,
) bool {
	return request.valid() && observation.PlanID == request.PlanID &&
		observation.PlanBindingDigest == request.PlanBindingDigest &&
		observation.PlanTransitionRevision == request.PlanTransitionRevision &&
		observation.SourceRevisionDigest == request.SourceRevisionDigest &&
		observation.CapabilityRevision == request.CapabilityRevision &&
		observation.PolicyRevision == request.PolicyRevision &&
		observation.FindingSetDigest == request.FindingSetDigest &&
		observation.TargetRootRevision == request.TargetRootRevision &&
		observation.TargetFilesystemRevision == request.TargetFilesystemRevision &&
		observation.TargetRevision == request.TargetRevision &&
		observation.RequiredBytes == request.RequiredBytes &&
		observation.RequiredInodes == request.RequiredInodes
}

func sanitizePreflightExternalEvidenceError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrTargetPreflightUnavailable
}

func (request PreflightExternalEvidenceRequest) valid() bool {
	return validOpaqueID(request.PlanID) && validDigest(request.PlanBindingDigest) &&
		request.PlanTransitionRevision > 0 && validDigest(request.SourceRevisionDigest) &&
		validOpaqueRevision(request.CapabilityRevision) && validOpaqueRevision(request.PolicyRevision) &&
		validDigest(request.FindingSetDigest) && validOpaqueRevision(request.TargetRootRevision) &&
		validOpaqueRevision(request.TargetFilesystemRevision) && validOpaqueRevision(request.TargetRevision) &&
		request.RequiredBytes >= 0 && request.RequiredInodes >= 0
}

func preflightExternalEvidenceRequestDigest(request PreflightExternalEvidenceRequest) string {
	if !request.valid() {
		return ""
	}
	return framedDigest(
		preflightExternalEvidenceRequestDomain,
		request.PlanID, request.PlanBindingDigest,
		strconv.FormatUint(request.PlanTransitionRevision, 10),
		request.SourceRevisionDigest, request.CapabilityRevision, request.PolicyRevision,
		request.FindingSetDigest, request.TargetRootRevision,
		request.TargetFilesystemRevision, request.TargetRevision,
		strconv.FormatInt(request.RequiredBytes, 10), strconv.FormatInt(request.RequiredInodes, 10),
	)
}

func preflightExternalEvidenceProofDigest(evidence PreflightExternalEvidence) string {
	if evidence.proof == nil || !validDigest(evidence.proof.requestDigest) {
		return ""
	}
	return framedDigest(
		preflightExternalEvidenceProofDomain,
		evidence.proof.requestDigest,
		strconv.FormatBool(evidence.proof.production),
		evidence.ObservedAt.UTC().Format(time.RFC3339Nano),
		evidence.ExpiresAt.UTC().Format(time.RFC3339Nano),
		evidence.SourceRevisionDigest, evidence.CapabilityRevision, evidence.PolicyRevision,
		evidence.FindingSetDigest, string(evidence.FindingDisposition),
		strconv.FormatBool(evidence.SourceAccessible),
		strconv.FormatBool(evidence.OverlapsXirangRoot),
		strconv.FormatBool(evidence.OverlapsSourceRoot),
		strconv.FormatInt(evidence.ReservedBytes, 10), strconv.FormatInt(evidence.ReservedInodes, 10),
	)
}

func (evidence PreflightExternalEvidence) validShape() bool {
	return !evidence.ObservedAt.IsZero() && !evidence.ExpiresAt.IsZero() &&
		!evidence.ExpiresAt.Before(evidence.ObservedAt) && validDigest(evidence.SourceRevisionDigest) &&
		validOpaqueRevision(evidence.CapabilityRevision) && validOpaqueRevision(evidence.PolicyRevision) &&
		validDigest(evidence.FindingSetDigest) && evidence.FindingDisposition.valid() &&
		evidence.ReservedBytes >= 0 && evidence.ReservedInodes >= 0
}

func (evidence PreflightExternalEvidence) ValidateFor(
	now time.Time,
	request PreflightExternalEvidenceRequest,
) error {
	if now.IsZero() || !request.valid() || evidence.proof == nil ||
		!evidence.validShape() || evidence.ObservedAt.After(now) || !evidence.ExpiresAt.After(now) ||
		evidence.proof.requestDigest != preflightExternalEvidenceRequestDigest(request) ||
		evidence.proof.bindingDigest != preflightExternalEvidenceProofDigest(evidence) {
		return ErrInvalidTargetPreflight
	}
	return nil
}

type TargetPreflightSnapshot struct {
	SchemaVersion          int                   `json:"schema_version"`
	ID                     string                `json:"id"`
	Revision               string                `json:"revision"`
	NodeID                 uint                  `json:"node_id"`
	TargetMode             TargetMode            `json:"target_mode"`
	ConflictPolicy         ConflictPolicy        `json:"conflict_policy"`
	NodeRevision           string                `json:"node_revision"`
	SourceRevisionDigest   string                `json:"source_revision_digest"`
	RootID                 string                `json:"root_id"`
	RootLocatorDigest      string                `json:"-"`
	PathDigest             string                `json:"-"`
	TargetRevision         string                `json:"target_revision"`
	RootRevision           string                `json:"root_revision"`
	FilesystemRevision     string                `json:"filesystem_revision"`
	CredentialRevision     string                `json:"credential_revision"`
	CapabilityRevision     string                `json:"capability_revision"`
	PolicyRevision         string                `json:"policy_revision"`
	FindingSetDigest       string                `json:"finding_set_digest"`
	OperationSetDigest     string                `json:"operation_set_digest"`
	DeleteSetDigest        string                `json:"delete_set_digest"`
	SecurityDecisionDigest string                `json:"security_decision_digest"`
	Impact                 RecoveryImpactSummary `json:"impact"`
	ObservedAt             time.Time             `json:"observed_at"`
	ExpiresAt              time.Time             `json:"expires_at"`
}

func (snapshot TargetPreflightSnapshot) ValidateAt(now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTargetPreflight
	}
	if !snapshot.ExpiresAt.After(now) {
		return ErrRecoveryPreflightConflict
	}
	if snapshot.SchemaVersion != 1 || !validOpaqueID(snapshot.ID) || !validOpaqueRevision(snapshot.Revision) ||
		snapshot.NodeID == 0 || snapshot.TargetMode.Validate() != nil || snapshot.ConflictPolicy.Validate() != nil ||
		!validOpaqueRevision(snapshot.NodeRevision) || !validDigest(snapshot.SourceRevisionDigest) ||
		!validBoundedOpaque(snapshot.RootID, targetRootIDMax) || !validDigest(snapshot.RootLocatorDigest) ||
		!validDigest(snapshot.PathDigest) ||
		!validOpaqueRevision(snapshot.TargetRevision) || !validOpaqueRevision(snapshot.RootRevision) ||
		!validOpaqueRevision(snapshot.FilesystemRevision) || !validOpaqueRevision(snapshot.CredentialRevision) ||
		!validOpaqueRevision(snapshot.CapabilityRevision) || !validOpaqueRevision(snapshot.PolicyRevision) ||
		!validDigest(snapshot.FindingSetDigest) || !validDigest(snapshot.OperationSetDigest) ||
		!validDigest(snapshot.DeleteSetDigest) || !validDigest(snapshot.SecurityDecisionDigest) ||
		snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) || snapshot.Impact.EstimatedItems < 0 ||
		snapshot.Impact.EstimatedBytes < 0 {
		return ErrInvalidTargetPreflight
	}
	if snapshot.ConflictPolicy == ConflictExactMirror && snapshot.TargetMode != TargetModeInPlace {
		return ErrInvalidTargetPreflight
	}
	return nil
}

type TargetPreflightResult struct {
	Eligible  bool                      `json:"eligible"`
	Preferred bool                      `json:"preferred"`
	Reasons   []TargetPreflightReason   `json:"reasons"`
	Snapshot  TargetPreflightSnapshot   `json:"snapshot"`
	Security  PreflightSecurityDecision `json:"security"`
	external  *preflightExternalEvidenceCommitBinding
}

type preflightExternalEvidenceCommitBinding struct {
	request  PreflightExternalEvidenceRequest
	evidence PreflightExternalEvidence
}

type TargetPreflightEvaluator struct {
	observer         TargetObservationPort
	externalEvidence PreflightExternalEvidencePort
}

// PreflightPersistenceRequest binds one read-only target observation to the
// exact draft-plan revision that may accept it.
type PreflightPersistenceRequest struct {
	RequesterID          uint
	PlanID               string
	ExpectedPlanRevision uint64
	Input                TargetPreflightInput
}

// PreflightPersistenceResult reports both ineligible read-only observations
// and the exact plan transition when a durable snapshot was committed.
type PreflightPersistenceResult struct {
	PlanID                 string
	Persisted              bool
	PlanTransitionRevision uint64
	Evaluation             TargetPreflightResult
}

type PreflightServiceDependencies struct {
	DB        *gorm.DB
	Now       func() time.Time
	Evaluator *TargetPreflightEvaluator
}

// PreflightService owns the observation-to-durable-snapshot commit boundary.
// Target I/O completes before its transaction begins.
type PreflightService struct {
	db              *gorm.DB
	now             func() time.Time
	evaluator       *TargetPreflightEvaluator
	sourceValidator *SourceValidator
}

func NewPreflightService(dependencies PreflightServiceDependencies) (*PreflightService, error) {
	if dependencies.DB == nil || dependencies.Evaluator == nil {
		return nil, ErrTargetPreflightUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	sourceValidator, err := NewSourceValidator(dependencies.DB)
	if err != nil {
		return nil, ErrTargetPreflightUnavailable
	}
	return &PreflightService{
		db: dependencies.DB, now: dependencies.Now, evaluator: dependencies.Evaluator,
		sourceValidator: sourceValidator,
	}, nil
}

func (service *PreflightService) EvaluateAndPersist(
	ctx context.Context,
	request PreflightPersistenceRequest,
) (PreflightPersistenceResult, error) {
	if service == nil || service.db == nil || service.evaluator == nil || service.sourceValidator == nil {
		return PreflightPersistenceResult{}, ErrTargetPreflightUnavailable
	}
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return PreflightPersistenceResult{}, err
	}
	now := service.now().UTC()
	if now.IsZero() || request.RequesterID == 0 || !validOpaqueID(request.PlanID) ||
		request.ExpectedPlanRevision == 0 {
		return PreflightPersistenceResult{}, ErrInvalidTargetPreflight
	}

	input, err := canonicalPreflightPersistenceInput(request.Input)
	if err != nil {
		return PreflightPersistenceResult{}, err
	}
	var observedPlan model.BackupAssetRecoveryPlan
	loaded := service.db.WithContext(ctx).
		Where("id = ? AND requester_id = ?", request.PlanID, request.RequesterID).
		Limit(1).Find(&observedPlan)
	if loaded.Error != nil {
		return PreflightPersistenceResult{}, preflightPersistenceDatabaseError(ctx)
	}
	if loaded.RowsAffected != 1 || validatePreflightPlanInput(observedPlan, request.ExpectedPlanRevision, input, now) != nil {
		return PreflightPersistenceResult{}, ErrRecoveryPreflightConflict
	}
	binding, err := newRecoveryTargetPreflightSessionBinding(observedPlan)
	if err != nil {
		return PreflightPersistenceResult{}, ErrRecoveryPreflightConflict
	}
	input.targetPermit = issueTargetPreflightPermit(input.Permit, binding, input.ProbeRequest)
	if input.targetPermit.ValidateRequestAt(now, input.Permit, input.ProbeRequest) != nil {
		return PreflightPersistenceResult{}, ErrRecoveryPreflightConflict
	}

	evaluation, err := service.evaluator.Evaluate(ctx, now, input)
	if err != nil {
		return PreflightPersistenceResult{}, err
	}
	result := PreflightPersistenceResult{PlanID: request.PlanID, Evaluation: evaluation}
	if !preflightObservationCommittable(evaluation) {
		return result, nil
	}
	encodedRows, candidateDigest, candidateCategories, err := validatePreflightCommitProduct(observedPlan, input, evaluation, now)
	if err != nil {
		return PreflightPersistenceResult{}, err
	}

	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedPlan model.BackupAssetRecoveryPlan
		locked := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND requester_id = ?", request.PlanID, request.RequesterID).
			Limit(1).Find(&lockedPlan)
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected != 1 || lockedPlan.BindingDigest != observedPlan.BindingDigest ||
			validatePreflightPlanInput(lockedPlan, request.ExpectedPlanRevision, input, now) != nil {
			return ErrRecoveryPreflightConflict
		}
		if err := service.sourceValidator.RevalidatePlanTx(ctx, tx, lockedPlan); err != nil {
			if errors.Is(err, ErrRecoverySourceChanged) || errors.Is(err, ErrInvalidRecoveryPlan) {
				return ErrRecoveryPreflightConflict
			}
			return err
		}
		txInput, err := canonicalPreflightPersistenceInput(input)
		if err != nil {
			return ErrRecoveryPreflightConflict
		}
		txEncodedRows, txCandidateDigest, txCandidateCategories, err :=
			validatePreflightCommitProduct(lockedPlan, txInput, evaluation, now)
		if err != nil || txEncodedRows != encodedRows || txCandidateDigest != candidateDigest ||
			txCandidateCategories != candidateCategories {
			return ErrRecoveryPreflightConflict
		}

		preflight := model.BackupAssetRecoveryPreflight{
			ID: evaluation.Snapshot.ID, PlanID: lockedPlan.ID, Revision: evaluation.Snapshot.Revision,
			SourceRevisionDigest:            evaluation.Snapshot.SourceRevisionDigest,
			TargetNodeID:                    evaluation.Snapshot.NodeID,
			NodeRevision:                    evaluation.Snapshot.NodeRevision,
			TargetRootID:                    evaluation.Snapshot.RootID,
			RootLocatorDigest:               evaluation.Snapshot.RootLocatorDigest,
			PathDigest:                      evaluation.Snapshot.PathDigest,
			TargetRevision:                  evaluation.Snapshot.TargetRevision,
			CapabilityRevision:              evaluation.Snapshot.CapabilityRevision,
			PolicyRevision:                  evaluation.Snapshot.PolicyRevision,
			FindingSetDigest:                evaluation.Snapshot.FindingSetDigest,
			SecurityOverrideCandidateDigest: candidateDigest,
			SecurityOverrideCategories:      candidateCategories,
			OperationSetDigest:              evaluation.Snapshot.OperationSetDigest,
			DeleteSetDigest:                 evaluation.Snapshot.DeleteSetDigest,
			EncryptedOperationRows:          encodedRows,
			EstimatedItems:                  evaluation.Snapshot.Impact.EstimatedItems,
			EstimatedBytes:                  evaluation.Snapshot.Impact.EstimatedBytes,
			ExpiresAt:                       evaluation.Snapshot.ExpiresAt.UTC(),
			CreatedAt:                       now,
		}
		if err := tx.WithContext(ctx).Create(&preflight).Error; err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
			Where("id = ? AND requester_id = ? AND state = ? AND transition_revision = ?",
				lockedPlan.ID, request.RequesterID, PlanStateDraft, request.ExpectedPlanRevision).
			Updates(map[string]any{
				"state":               string(PlanStatePreflightReady),
				"transition_revision": request.ExpectedPlanRevision + 1,
				"updated_at":          now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryPreflightConflict
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryPreflightConflict) {
			return PreflightPersistenceResult{}, ErrRecoveryPreflightConflict
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PreflightPersistenceResult{}, ctxErr
		}
		return PreflightPersistenceResult{}, ErrTargetPreflightUnavailable
	}
	result.Persisted = true
	result.PlanTransitionRevision = request.ExpectedPlanRevision + 1
	return result, nil
}

func canonicalPreflightPersistenceInput(input TargetPreflightInput) (TargetPreflightInput, error) {
	if err := validatePreflightOperationProducts(input); err != nil {
		return TargetPreflightInput{}, err
	}
	rebuilt, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: input.TargetMode, ConflictPolicy: input.ConflictPolicy,
		Operations: input.Operations.Rows,
		Limits: RecoveryOperationLimits{
			MaxRows: len(input.Operations.Rows), MaxItems: len(input.Operations.Rows),
			MaxBytes: input.Operations.Impact.EstimatedBytes, MaxImpactRows: len(input.Operations.Rows),
		},
	})
	if err != nil {
		return TargetPreflightInput{}, ErrInvalidTargetPreflight
	}
	result := input
	result.Operations = rebuilt
	result.Security = clonePreflightSecurityDecision(input.Security)
	result.targetPermit = TargetPreflightPermit{}
	return result, nil
}

func validatePreflightPlanInput(
	plan model.BackupAssetRecoveryPlan,
	expectedRevision uint64,
	input TargetPreflightInput,
	now time.Time,
) error {
	if PlanState(plan.State) != PlanStateDraft || plan.TransitionRevision != expectedRevision ||
		!plan.PreflightExpiresAt.UTC().After(now) || input.SnapshotRevision != plan.PreflightRevision ||
		input.Node.NodeID != plan.TargetNodeID || input.Node.NodeRevision != plan.TargetBaseRevision ||
		input.Frozen.NodeRevision != plan.TargetBaseRevision ||
		input.Frozen.SourceRevisionDigest != plan.SourceRevisionDigest ||
		input.Frozen.TargetRevision != plan.TargetBaseRevision ||
		input.Frozen.CapabilityRevision != plan.CapabilityRevision ||
		input.Frozen.PolicyRevision != plan.SecurityPolicyRevision ||
		input.Frozen.FindingSetDigest != plan.SecurityFindingSetDigest ||
		input.TargetMode != TargetMode(plan.TargetMode) ||
		input.ConflictPolicy != ConflictPolicy(plan.ConflictPolicy) ||
		input.Operations.OperationSetDigest != plan.OperationSetDigest ||
		input.Operations.DeleteSetDigest != plan.DeleteSetDigest ||
		input.Operations.Impact.EstimatedItems != plan.EstimatedItems ||
		input.Operations.Impact.EstimatedBytes != plan.EstimatedBytes ||
		string(input.Security.Decision.Kind) != plan.SecurityDecision ||
		input.Security.Decision.DecisionDigest != plan.SecurityDecisionDigest ||
		input.Security.Decision.FindingSetDigest != plan.SecurityFindingSetDigest ||
		input.Security.Decision.PolicyRevision != plan.SecurityPolicyRevision ||
		input.Permit.NodeID != plan.TargetNodeID || input.Permit.Purpose != TargetPurposePreflight ||
		input.Permit.RootID != plan.TargetRootID || input.Permit.RootLocatorDigest != plan.RootLocatorDigest ||
		input.Permit.TargetPathDigest != plan.PathDigest || input.Permit.RootRevision != plan.RootRevision ||
		!input.Permit.ExpiresAt.UTC().Equal(plan.PreflightExpiresAt.UTC()) ||
		input.ProbeRequest.Object.RootID != plan.TargetRootID ||
		input.ProbeRequest.Object.RootLocatorDigest != plan.RootLocatorDigest ||
		input.ProbeRequest.Object.TargetPathDigest != plan.PathDigest ||
		input.ProbeRequest.Object.PrivateRelativeLocator != plan.EncryptedTargetRelativePath ||
		input.ProbeRequest.SourceRevisionDigest != plan.SourceRevisionDigest ||
		input.ProbeRequest.CapabilityRevision != plan.CapabilityRevision ||
		input.ProbeRequest.PolicyRevision != plan.SecurityPolicyRevision ||
		input.ProbeRequest.RequiredBytes != plan.EstimatedBytes ||
		input.ProbeRequest.RequiredInodes != plan.EstimatedItems {
		return ErrRecoveryPreflightConflict
	}
	if err := input.Security.validateBindingShape(plan.SecurityFindingSetDigest, plan.SecurityPolicyRevision); err != nil {
		return ErrRecoveryPreflightConflict
	}
	return nil
}

func preflightObservationCommittable(evaluation TargetPreflightResult) bool {
	if evaluation.Eligible {
		return len(evaluation.Reasons) == 0
	}
	return len(evaluation.Reasons) == 1 && evaluation.Reasons[0] == TargetPreflightSecurityBlocked
}

func validatePreflightCommitProduct(
	plan model.BackupAssetRecoveryPlan,
	input TargetPreflightInput,
	evaluation TargetPreflightResult,
	now time.Time,
) (string, string, string, error) {
	snapshot := evaluation.Snapshot
	if err := validateProductionPreflightExternalEvidence(plan, input, evaluation, now); err != nil {
		return "", "", "", err
	}
	if snapshot.ValidateAt(now) != nil || snapshot.ID != input.SnapshotID ||
		snapshot.Revision != plan.PreflightRevision || snapshot.NodeID != plan.TargetNodeID ||
		snapshot.TargetMode != TargetMode(plan.TargetMode) ||
		snapshot.ConflictPolicy != ConflictPolicy(plan.ConflictPolicy) ||
		snapshot.NodeRevision != plan.TargetBaseRevision ||
		snapshot.SourceRevisionDigest != plan.SourceRevisionDigest || snapshot.RootID != plan.TargetRootID ||
		snapshot.RootLocatorDigest != plan.RootLocatorDigest || snapshot.PathDigest != plan.PathDigest ||
		snapshot.TargetRevision != plan.TargetBaseRevision || snapshot.RootRevision != plan.RootRevision ||
		snapshot.FilesystemRevision != plan.FilesystemRevision ||
		snapshot.CredentialRevision != plan.CredentialScopeRevision ||
		snapshot.CapabilityRevision != plan.CapabilityRevision ||
		snapshot.PolicyRevision != plan.SecurityPolicyRevision ||
		snapshot.FindingSetDigest != plan.SecurityFindingSetDigest ||
		snapshot.OperationSetDigest != plan.OperationSetDigest || snapshot.DeleteSetDigest != plan.DeleteSetDigest ||
		snapshot.SecurityDecisionDigest != plan.SecurityDecisionDigest ||
		!snapshot.ExpiresAt.UTC().Equal(plan.PreflightExpiresAt.UTC()) ||
		!samePreflightImpact(snapshot.Impact, input.Operations.Impact) ||
		!samePreflightSecurityDecision(evaluation.Security, input.Security) {
		return "", "", "", ErrRecoveryPreflightConflict
	}
	encodedRows, err := encodeRecoveryOperationRows(input.Operations.Rows)
	if err != nil {
		return "", "", "", ErrRecoveryPreflightConflict
	}
	candidateDigest, candidateCategories, err := preflightOverrideCandidateFields(input.Security)
	if err != nil {
		return "", "", "", ErrRecoveryPreflightConflict
	}
	return encodedRows, candidateDigest, candidateCategories, nil
}

func validateProductionPreflightExternalEvidence(
	plan model.BackupAssetRecoveryPlan,
	input TargetPreflightInput,
	evaluation TargetPreflightResult,
	now time.Time,
) error {
	binding := evaluation.external
	if binding == nil || binding.evidence.proof == nil || !binding.evidence.proof.production {
		return ErrInvalidTargetPreflight
	}
	expected := PreflightExternalEvidenceRequest{
		PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
		PlanTransitionRevision:   plan.TransitionRevision,
		SourceRevisionDigest:     plan.SourceRevisionDigest,
		CapabilityRevision:       plan.CapabilityRevision,
		PolicyRevision:           plan.SecurityPolicyRevision,
		FindingSetDigest:         plan.SecurityFindingSetDigest,
		TargetRootRevision:       evaluation.Snapshot.RootRevision,
		TargetFilesystemRevision: evaluation.Snapshot.FilesystemRevision,
		TargetRevision:           evaluation.Snapshot.TargetRevision,
		RequiredBytes:            plan.EstimatedBytes,
		RequiredInodes:           plan.EstimatedItems,
	}
	if binding.request != expected || binding.evidence.ValidateFor(now, expected) != nil ||
		evaluation.Snapshot.ExpiresAt.After(binding.evidence.ExpiresAt) ||
		input.Security.ValidateBinding(
			binding.evidence.FindingSetDigest,
			binding.evidence.PolicyRevision,
			binding.evidence.FindingDisposition,
		) != nil {
		return ErrRecoveryPreflightConflict
	}
	return nil
}

func preflightOverrideCandidateFields(security PreflightSecurityDecision) (string, string, error) {
	if err := security.validateBindingShape(
		security.Decision.FindingSetDigest,
		security.Decision.PolicyRevision,
	); err != nil {
		return "", "", err
	}
	if security.OverrideCandidate == nil {
		return "", "", nil
	}
	categories := make([]string, len(security.OverrideCandidate.Categories))
	for index, category := range security.OverrideCandidate.Categories {
		categories[index] = string(category)
	}
	return security.OverrideCandidate.BindingDigest, strings.Join(categories, ","), nil
}

func samePreflightImpact(left, right RecoveryImpactSummary) bool {
	if left.CreateCount != right.CreateCount || left.OverwriteCount != right.OverwriteCount ||
		left.SkipCount != right.SkipCount || left.DeleteCount != right.DeleteCount ||
		left.EstimatedItems != right.EstimatedItems || left.EstimatedBytes != right.EstimatedBytes ||
		len(left.Rows) != len(right.Rows) {
		return false
	}
	for index := range left.Rows {
		if left.Rows[index] != right.Rows[index] {
			return false
		}
	}
	return true
}

func samePreflightSecurityDecision(left, right PreflightSecurityDecision) bool {
	if left.Decision != right.Decision || left.FindingCount != right.FindingCount {
		return false
	}
	leftDigest, leftCategories, leftErr := preflightOverrideCandidateFields(left)
	rightDigest, rightCategories, rightErr := preflightOverrideCandidateFields(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest && leftCategories == rightCategories
}

func preflightPersistenceDatabaseError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrTargetPreflightUnavailable
}

func NewTargetPreflightEvaluator(
	observer TargetObservationPort,
	externalEvidence PreflightExternalEvidencePort,
) (*TargetPreflightEvaluator, error) {
	if observer == nil || externalEvidence == nil {
		return nil, ErrInvalidTargetPreflight
	}
	return &TargetPreflightEvaluator{observer: observer, externalEvidence: externalEvidence}, nil
}

func (evaluator *TargetPreflightEvaluator) Evaluate(
	ctx context.Context,
	now time.Time,
	input TargetPreflightInput,
) (TargetPreflightResult, error) {
	if evaluator == nil || evaluator.observer == nil || evaluator.externalEvidence == nil ||
		now.IsZero() || input.SnapshotTTL <= 0 ||
		!validOpaqueID(input.SnapshotID) || !validOpaqueRevision(input.SnapshotRevision) || input.Node.NodeID == 0 ||
		!validOpaqueRevision(input.Frozen.NodeRevision) || !validDigest(input.Frozen.SourceRevisionDigest) ||
		!validOpaqueRevision(input.Frozen.TargetRevision) || !validOpaqueRevision(input.Frozen.CapabilityRevision) ||
		!validOpaqueRevision(input.Frozen.PolicyRevision) || !validDigest(input.Frozen.FindingSetDigest) ||
		input.TargetMode.Validate() != nil || input.ConflictPolicy.Validate() != nil ||
		input.ProbeRequest.RequiredBytes < 0 || input.ProbeRequest.RequiredInodes < 0 ||
		!input.ProbeRequest.Object.valid() {
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}
	if input.ConflictPolicy == ConflictExactMirror && input.TargetMode != TargetModeInPlace {
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}
	if input.Node.NodeRevision != input.Frozen.NodeRevision ||
		input.ProbeRequest.SourceRevisionDigest != input.Frozen.SourceRevisionDigest ||
		input.ProbeRequest.CapabilityRevision != input.Frozen.CapabilityRevision ||
		input.ProbeRequest.PolicyRevision != input.Frozen.PolicyRevision {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	if err := input.Security.validateBindingShape(input.Frozen.FindingSetDigest, input.Frozen.PolicyRevision); err != nil {
		if errors.Is(err, ErrRecoveryPreflightConflict) {
			return TargetPreflightResult{}, err
		}
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}
	if err := validatePreflightOperationProducts(input); err != nil {
		return TargetPreflightResult{}, err
	}
	if !input.Permit.ExpiresAt.After(now) {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	if input.targetPermit.proof == nil || input.targetPermit.ValidateAt(now) != nil {
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}
	if input.targetPermit.ValidateRequestAt(now, input.Permit, input.ProbeRequest) != nil ||
		input.Permit.NodeID != input.Node.NodeID {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	binding := input.targetPermit.proof.sessionBinding

	preProbeReasons := nodePreflightReasons(input.Node)
	if len(preProbeReasons) > 0 {
		return TargetPreflightResult{
			Reasons:  preProbeReasons,
			Security: clonePreflightSecurityDecision(input.Security),
		}, nil
	}

	targetFacts, err := evaluator.observer.ProbeRoot(ctx, input.targetPermit, input.ProbeRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return TargetPreflightResult{}, err
		}
		return TargetPreflightResult{}, ErrTargetPreflightUnavailable
	}
	if !targetFacts.ExpiresAt.After(now) {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	if targetFacts.ObservedAt.IsZero() || targetFacts.ObservedAt.After(now) ||
		!validOpaqueRevision(targetFacts.RootRevision) ||
		!validOpaqueRevision(targetFacts.FilesystemRevision) ||
		!validOpaqueRevision(targetFacts.CredentialRevision) ||
		!validOpaqueRevision(targetFacts.TargetRevision) ||
		targetFacts.FreeBytes < 0 || targetFacts.FreeInodes < 0 {
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}
	if targetFacts.RootRevision != input.Permit.RootRevision ||
		targetFacts.RootRevision != binding.rootRevision ||
		targetFacts.FilesystemRevision != binding.filesystemRevision ||
		targetFacts.CredentialRevision != binding.credentialRevision ||
		targetFacts.TargetRevision != input.Frozen.TargetRevision ||
		targetFacts.TargetRevision != binding.targetRevision {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}

	externalRequest := PreflightExternalEvidenceRequest{
		PlanID: binding.planID, PlanBindingDigest: binding.planBindingDigest,
		PlanTransitionRevision:   binding.planTransitionRevision,
		SourceRevisionDigest:     input.Frozen.SourceRevisionDigest,
		CapabilityRevision:       input.Frozen.CapabilityRevision,
		PolicyRevision:           input.Frozen.PolicyRevision,
		FindingSetDigest:         input.Frozen.FindingSetDigest,
		TargetRootRevision:       targetFacts.RootRevision,
		TargetFilesystemRevision: targetFacts.FilesystemRevision,
		TargetRevision:           targetFacts.TargetRevision,
		RequiredBytes:            input.ProbeRequest.RequiredBytes,
		RequiredInodes:           input.ProbeRequest.RequiredInodes,
	}
	externalFacts, err := evaluator.externalEvidence.ObservePreflightEvidence(ctx, externalRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return TargetPreflightResult{}, err
		}
		if errors.Is(err, ErrRecoveryPreflightConflict) {
			return TargetPreflightResult{}, ErrRecoveryPreflightConflict
		}
		if errors.Is(err, ErrInvalidTargetPreflight) {
			return TargetPreflightResult{}, ErrInvalidTargetPreflight
		}
		return TargetPreflightResult{}, ErrTargetPreflightUnavailable
	}
	if !externalFacts.ExpiresAt.After(now) {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	if err := externalFacts.ValidateFor(now, externalRequest); err != nil {
		return TargetPreflightResult{}, err
	}
	if externalFacts.SourceRevisionDigest != input.Frozen.SourceRevisionDigest ||
		externalFacts.CapabilityRevision != input.Frozen.CapabilityRevision ||
		externalFacts.PolicyRevision != input.Frozen.PolicyRevision ||
		externalFacts.FindingSetDigest != input.Frozen.FindingSetDigest {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	if err := input.Security.ValidateBinding(
		externalFacts.FindingSetDigest,
		externalFacts.PolicyRevision,
		externalFacts.FindingDisposition,
	); err != nil {
		if errors.Is(err, ErrRecoveryPreflightConflict) {
			return TargetPreflightResult{}, err
		}
		return TargetPreflightResult{}, ErrInvalidTargetPreflight
	}

	reasons := observedPreflightReasons(input, targetFacts, externalFacts)
	eligible := len(reasons) == 0
	expiresAt := now.Add(input.SnapshotTTL)
	for _, candidate := range []time.Time{targetFacts.ExpiresAt, input.Permit.ExpiresAt, externalFacts.ExpiresAt} {
		if candidate.Before(expiresAt) {
			expiresAt = candidate
		}
	}
	if !expiresAt.After(now) {
		return TargetPreflightResult{}, ErrRecoveryPreflightConflict
	}
	observedAt := targetFacts.ObservedAt
	if externalFacts.ObservedAt.After(observedAt) {
		observedAt = externalFacts.ObservedAt
	}
	snapshot := TargetPreflightSnapshot{
		SchemaVersion: 1, ID: input.SnapshotID, Revision: input.SnapshotRevision, NodeID: input.Node.NodeID,
		TargetMode: input.TargetMode, ConflictPolicy: input.ConflictPolicy,
		NodeRevision: input.Frozen.NodeRevision, SourceRevisionDigest: input.Frozen.SourceRevisionDigest,
		RootID: input.ProbeRequest.Object.RootID, RootLocatorDigest: input.ProbeRequest.Object.RootLocatorDigest,
		PathDigest:     input.ProbeRequest.Object.TargetPathDigest,
		TargetRevision: input.Frozen.TargetRevision, RootRevision: targetFacts.RootRevision,
		FilesystemRevision: targetFacts.FilesystemRevision, CredentialRevision: targetFacts.CredentialRevision,
		CapabilityRevision: input.Frozen.CapabilityRevision, PolicyRevision: input.Frozen.PolicyRevision,
		FindingSetDigest: input.Frozen.FindingSetDigest, OperationSetDigest: input.Operations.OperationSetDigest,
		DeleteSetDigest: input.Operations.DeleteSetDigest, SecurityDecisionDigest: input.Security.Decision.DecisionDigest,
		Impact: cloneRecoveryImpactSummary(input.Operations.Impact), ObservedAt: observedAt, ExpiresAt: expiresAt,
	}
	return TargetPreflightResult{
		Eligible: eligible, Preferred: eligible && input.Node.ProducingNode,
		Reasons: append([]TargetPreflightReason(nil), reasons...), Snapshot: snapshot,
		Security: clonePreflightSecurityDecision(input.Security),
		external: &preflightExternalEvidenceCommitBinding{
			request: externalRequest, evidence: clonePreflightExternalEvidence(externalFacts),
		},
	}, nil
}

func clonePreflightExternalEvidence(evidence PreflightExternalEvidence) PreflightExternalEvidence {
	clone := evidence
	if evidence.proof != nil {
		proof := *evidence.proof
		clone.proof = &proof
	}
	return clone
}

func validatePreflightOperationProducts(input TargetPreflightInput) error {
	if len(input.Operations.Rows) == 0 || input.Operations.Impact.EstimatedBytes < 0 {
		return ErrInvalidTargetPreflight
	}
	rebuilt, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: input.TargetMode, ConflictPolicy: input.ConflictPolicy,
		Operations: input.Operations.Rows,
		Limits: RecoveryOperationLimits{
			MaxRows: len(input.Operations.Rows), MaxItems: len(input.Operations.Rows),
			MaxBytes: input.Operations.Impact.EstimatedBytes, MaxImpactRows: len(input.Operations.Rows),
		},
	})
	if err != nil || rebuilt.OperationSetDigest != input.Operations.OperationSetDigest ||
		rebuilt.DeleteSetDigest != input.Operations.DeleteSetDigest ||
		rebuilt.Impact.CreateCount != input.Operations.Impact.CreateCount ||
		rebuilt.Impact.OverwriteCount != input.Operations.Impact.OverwriteCount ||
		rebuilt.Impact.SkipCount != input.Operations.Impact.SkipCount ||
		rebuilt.Impact.DeleteCount != input.Operations.Impact.DeleteCount ||
		rebuilt.Impact.EstimatedItems != input.Operations.Impact.EstimatedItems ||
		rebuilt.Impact.EstimatedBytes != input.Operations.Impact.EstimatedBytes {
		return ErrInvalidTargetPreflight
	}
	return nil
}

func nodePreflightReasons(node NodeEligibilityFacts) []TargetPreflightReason {
	reasons := make([]TargetPreflightReason, 0, 5)
	if !node.Registered {
		reasons = append(reasons, TargetPreflightNodeUnregistered)
	}
	if node.Archived {
		reasons = append(reasons, TargetPreflightNodeArchived)
	}
	if !node.Online {
		reasons = append(reasons, TargetPreflightNodeOffline)
	}
	if !node.Authorized {
		reasons = append(reasons, TargetPreflightNodeUnauthorized)
	}
	if node.CredentialPurpose != TargetPurposePreflight {
		reasons = append(reasons, TargetPreflightCredentialPurpose)
	}
	return reasons
}

func observedPreflightReasons(
	input TargetPreflightInput,
	targetFacts TargetRootProbeFacts,
	externalFacts PreflightExternalEvidence,
) []TargetPreflightReason {
	reasons := make([]TargetPreflightReason, 0, 16)
	if !targetFacts.RequiredToolsAvailable {
		reasons = append(reasons, TargetPreflightToolUnavailable)
	}
	if !externalFacts.SourceAccessible {
		reasons = append(reasons, TargetPreflightSourceUnavailable)
	}
	if !targetFacts.RootReal {
		reasons = append(reasons, TargetPreflightRootNotReal)
	}
	if !targetFacts.RootCanonical {
		reasons = append(reasons, TargetPreflightRootNoncanonical)
	}
	if !targetFacts.DeviceValid {
		reasons = append(reasons, TargetPreflightDeviceInvalid)
	}
	if !targetFacts.MountValid {
		reasons = append(reasons, TargetPreflightMountInvalid)
	}
	if !targetFacts.OwnerValid {
		reasons = append(reasons, TargetPreflightOwnerInvalid)
	}
	if !targetFacts.ModeValid {
		reasons = append(reasons, TargetPreflightModeInvalid)
	}
	if targetFacts.HasSymlinkComponent {
		reasons = append(reasons, TargetPreflightSymlinkComponent)
	}
	if externalFacts.OverlapsXirangRoot {
		reasons = append(reasons, TargetPreflightXirangOverlap)
	}
	if externalFacts.OverlapsSourceRoot {
		reasons = append(reasons, TargetPreflightSourceOverlap)
	}
	if targetFacts.FreeBytes < externalFacts.ReservedBytes ||
		input.ProbeRequest.RequiredBytes > targetFacts.FreeBytes-externalFacts.ReservedBytes {
		reasons = append(reasons, TargetPreflightInsufficientBytes)
	}
	if targetFacts.FreeInodes < externalFacts.ReservedInodes ||
		input.ProbeRequest.RequiredInodes > targetFacts.FreeInodes-externalFacts.ReservedInodes {
		reasons = append(reasons, TargetPreflightInsufficientInodes)
	}
	if input.Node.ActiveWriter {
		reasons = append(reasons, TargetPreflightActiveWriter)
	}
	if targetFacts.TargetExists && (input.TargetMode == TargetModeIsolated || input.ConflictPolicy == ConflictFailOnConflict) {
		reasons = append(reasons, TargetPreflightTargetConflict)
	}
	if input.Security.Decision.Kind != SecurityDecisionAllowClean {
		reasons = append(reasons, TargetPreflightSecurityBlocked)
	}
	return reasons
}

func cloneRecoveryImpactSummary(impact RecoveryImpactSummary) RecoveryImpactSummary {
	clone := impact
	clone.Rows = append([]RecoveryImpactRow(nil), impact.Rows...)
	return clone
}

func clonePreflightSecurityDecision(product PreflightSecurityDecision) PreflightSecurityDecision {
	clone := product
	if product.OverrideCandidate != nil {
		candidate := *product.OverrideCandidate
		candidate.Categories = append([]SecurityFindingCategory(nil), product.OverrideCandidate.Categories...)
		clone.OverrideCandidate = &candidate
	}
	return clone
}
