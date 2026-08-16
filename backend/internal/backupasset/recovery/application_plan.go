package recovery

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type RecoveryApplicationPlanRequest struct {
	RequesterID      uint
	PlanID           string
	ExpectedRevision uint64
	ObservedAt       time.Time
}

type RecoveryApplicationPlanSnapshot struct {
	PlanID             string
	RequesterID        uint
	TransitionRevision uint64
	BindingDigest      string
	Selection          ExactSelection
	Target             TargetBinding
	ConflictPolicy     ConflictPolicy
	OperationSetDigest string
	DeleteSetDigest    string
	CapabilityRevision string
	SecurityDecision   SecurityDecision
	PreflightRevision  string
	PreflightExpiresAt time.Time
	EstimatedItems     int64
	EstimatedBytes     int64
	ActiveWriter       bool
}

func (RecoveryApplicationPlanRequest) String() string   { return "[recovery application plan request]" }
func (RecoveryApplicationPlanRequest) GoString() string { return "[recovery application plan request]" }
func (RecoveryApplicationPlanSnapshot) String() string  { return "[recovery application plan snapshot]" }
func (RecoveryApplicationPlanSnapshot) GoString() string {
	return "[recovery application plan snapshot]"
}

type RecoveryApplicationPlanAuthority interface {
	LoadRecoveryApplicationPlan(context.Context, RecoveryApplicationPlanRequest) (RecoveryApplicationPlanSnapshot, error)
}

type recoveryApplicationPlanAuthority struct {
	db        *gorm.DB
	validator *SourceValidator
}

func NewRecoveryApplicationPlanAuthority(db *gorm.DB) (RecoveryApplicationPlanAuthority, error) {
	validator, err := NewSourceValidator(db)
	if err != nil {
		return nil, ErrRecoveryPlanUnavailable
	}
	return &recoveryApplicationPlanAuthority{db: db, validator: validator}, nil
}

func (authority *recoveryApplicationPlanAuthority) LoadRecoveryApplicationPlan(
	ctx context.Context,
	request RecoveryApplicationPlanRequest,
) (RecoveryApplicationPlanSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RecoveryApplicationPlanSnapshot{}, err
	}
	if authority == nil || authority.db == nil || authority.validator == nil || request.RequesterID == 0 ||
		!validOpaqueID(request.PlanID) || request.ExpectedRevision == 0 || request.ObservedAt.IsZero() {
		return RecoveryApplicationPlanSnapshot{}, ErrInvalidTargetPreflight
	}
	var snapshot RecoveryApplicationPlanSnapshot
	err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan model.BackupAssetRecoveryPlan
		loaded := tx.WithContext(ctx).Where("id = ? AND requester_id = ?", request.PlanID, request.RequesterID).
			Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryAPIObjectNotFound
		}
		if PlanState(plan.State) != PlanStateDraft || plan.TransitionRevision != request.ExpectedRevision ||
			!plan.PreflightExpiresAt.UTC().After(request.ObservedAt.UTC()) {
			return ErrRecoveryPreflightConflict
		}
		selection, err := authority.reconstructSelectionTx(ctx, tx, plan)
		if err != nil {
			return err
		}
		decision := SecurityDecision{
			Kind: SecurityDecisionKind(plan.SecurityDecision), DecisionDigest: plan.SecurityDecisionDigest,
			FindingSetDigest: plan.SecurityFindingSetDigest, PolicyRevision: plan.SecurityPolicyRevision,
			OverrideBindingDigest: plan.SecurityOverrideBindingDigest,
		}
		target := TargetBinding{
			Mode: TargetMode(plan.TargetMode), NodeID: plan.TargetNodeID, RootID: plan.TargetRootID,
			EncryptedRelativePath: plan.EncryptedTargetRelativePath,
			RootLocatorDigest:     plan.RootLocatorDigest, PathDigest: plan.PathDigest,
			BaseNodeRevision: plan.TargetBaseRevision, CredentialScopeRevision: plan.CredentialScopeRevision,
			RootRevision: plan.RootRevision, FilesystemRevision: plan.FilesystemRevision,
		}
		if !validDigest(plan.BindingDigest) || target.Validate() != nil || decision.Validate() != nil ||
			ConflictPolicy(plan.ConflictPolicy).Validate() != nil || !validDigest(plan.OperationSetDigest) ||
			!validDigest(plan.DeleteSetDigest) || !validOpaqueRevision(plan.CapabilityRevision) ||
			!validOpaqueRevision(plan.PreflightRevision) || plan.EstimatedItems <= 0 || plan.EstimatedBytes < 0 {
			return ErrRecoveryPlanUnavailable
		}
		var activeWriters int64
		active := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
			Where("node_id = ? AND released_at IS NULL AND lease_expires_at > ?", plan.TargetNodeID, request.ObservedAt.UTC()).
			Limit(1).Count(&activeWriters)
		if active.Error != nil {
			return active.Error
		}
		snapshot = RecoveryApplicationPlanSnapshot{
			PlanID: plan.ID, RequesterID: plan.RequesterID, TransitionRevision: plan.TransitionRevision,
			BindingDigest: plan.BindingDigest, Selection: selection, Target: target,
			ConflictPolicy: ConflictPolicy(plan.ConflictPolicy), OperationSetDigest: plan.OperationSetDigest,
			DeleteSetDigest: plan.DeleteSetDigest, CapabilityRevision: plan.CapabilityRevision,
			SecurityDecision: decision, PreflightRevision: plan.PreflightRevision,
			PreflightExpiresAt: plan.PreflightExpiresAt.UTC(), EstimatedItems: plan.EstimatedItems,
			EstimatedBytes: plan.EstimatedBytes, ActiveWriter: activeWriters > 0,
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrRecoveryAPIObjectNotFound):
			return RecoveryApplicationPlanSnapshot{}, ErrRecoveryAPIObjectNotFound
		case errors.Is(err, ErrRecoveryPreflightConflict):
			return RecoveryApplicationPlanSnapshot{}, ErrRecoveryPreflightConflict
		case errors.Is(err, ErrRecoverySourceChanged), errors.Is(err, ErrInvalidRecoveryPlan):
			return RecoveryApplicationPlanSnapshot{}, ErrRecoverySourceChanged
		default:
			if ctx.Err() != nil {
				return RecoveryApplicationPlanSnapshot{}, ctx.Err()
			}
			return RecoveryApplicationPlanSnapshot{}, ErrTargetPreflightUnavailable
		}
	}
	return snapshot, nil
}

func (authority *recoveryApplicationPlanAuthority) reconstructSelectionTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
) (ExactSelection, error) {
	if err := authority.validator.RevalidatePlanTx(ctx, tx, plan); err != nil {
		return ExactSelection{}, err
	}
	var repository struct {
		ID           string `gorm:"column:id"`
		ProviderKind string `gorm:"column:provider_kind"`
	}
	loaded := tx.WithContext(ctx).Table("backup_repositories").
		Select("id, provider_kind").Where("id = ?", plan.RepositoryID).Limit(1).Find(&repository)
	provider := backupasset.ProviderKind(repository.ProviderKind)
	if loaded.Error != nil || loaded.RowsAffected != 1 || repository.ID != plan.RepositoryID ||
		!validRecoveryProvider(provider) {
		return ExactSelection{}, ErrRecoverySourceChanged
	}
	locatorDigest, err := SourceLocatorDigest(plan.RepositoryID, provider, plan.RecoveryPointID, plan.EncryptedSourceLocator)
	if err != nil {
		return ExactSelection{}, ErrRecoverySourceChanged
	}
	revision, err := recoveryPlanSourceRevision(plan)
	if err != nil {
		return ExactSelection{}, err
	}
	var items []model.BackupAssetRecoveryPlanItem
	if err := tx.WithContext(ctx).Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		return ExactSelection{}, err
	}
	refs := make([]backupasset.AssetRef, len(items))
	for ordinal, item := range items {
		if item.PlanID != plan.ID || item.Ordinal != ordinal || item.RecoveryPointID != plan.RecoveryPointID ||
			item.CatalogGenerationID != plan.CatalogGenerationID {
			return ExactSelection{}, ErrRecoverySourceChanged
		}
		refs[ordinal] = backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID}
	}
	selection, err := newExactSelectionWithPrivateSourceBinding(ExactSelectionInput{
		RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID,
		CatalogGenerationID: plan.CatalogGenerationID, AssetRefs: refs, SourceRevision: revision,
	}, frozenSourceBinding{Provider: provider, LocatorDigest: locatorDigest})
	if err != nil || selection.SelectionDigest != plan.SelectionDigest ||
		selection.SourceRevisionDigest != plan.SourceRevisionDigest {
		return ExactSelection{}, ErrRecoverySourceChanged
	}
	return selection, nil
}

var _ RecoveryApplicationPlanAuthority = (*recoveryApplicationPlanAuthority)(nil)
