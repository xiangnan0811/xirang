package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrProcessingDisabled                   = errors.New("backup asset processing disabled")
	ErrProcessingHandleNotFound             = errors.New("backup asset processing handle not found")
	ErrProcessingPublicationIdentityChanged = errors.New("backup asset processing publication identity changed")
)

type PreviewRepresentation string

const (
	PreviewThumbnail     PreviewRepresentation = "thumbnail"
	PreviewText          PreviewRepresentation = "text"
	PreviewDocumentPages PreviewRepresentation = "document_pages"
	PreviewMedia         PreviewRepresentation = "media_preview"
	PreviewArchiveIndex  PreviewRepresentation = "archive_index"
)

const previewInterestCompensationTimeout = 5 * time.Second

type ProcessingProductState string

const (
	ProcessingProductNative      ProcessingProductState = "native"
	ProcessingProductDerived     ProcessingProductState = "derived"
	ProcessingProductPartial     ProcessingProductState = "partial"
	ProcessingProductUnsupported ProcessingProductState = "unsupported"
	ProcessingProductNotDeployed ProcessingProductState = "not_deployed"
	ProcessingProductQueued      ProcessingProductState = "queued"
	ProcessingProductFailed      ProcessingProductState = "failed"
)

type PreviewJobRequest struct {
	Actor          content.DeliveryActor
	Ref            backupasset.AssetRef
	Representation PreviewRepresentation
	Profile        string
}

type PreviewJobLookup struct {
	Actor content.DeliveryActor
	Ref   backupasset.AssetRef
	JobID string
}

type PreviewStateRequest struct {
	Actor content.DeliveryActor
	Ref   backupasset.AssetRef
}

type AssetProcessingState struct {
	SchemaVersion   int                `json:"schema_version"`
	Representations []PreviewJobResult `json:"representations"`
}

type PreviewJobResult struct {
	SchemaVersion     int                    `json:"schema_version"`
	JobID             string                 `json:"job_id,omitempty"`
	State             ProcessingProductState `json:"state"`
	Representation    PreviewRepresentation  `json:"representation"`
	Capability        string                 `json:"capability,omitempty"`
	Profile           string                 `json:"profile,omitempty"`
	Coverage          string                 `json:"coverage,omitempty"`
	Freshness         string                 `json:"freshness,omitempty"`
	ScanStatus        string                 `json:"scan_status,omitempty"`
	SensitivityStatus string                 `json:"sensitivity_status,omitempty"`
	Reason            string                 `json:"reason,omitempty"`
	Retryable         bool                   `json:"retryable"`
	FallbackActions   []string               `json:"fallback_actions"`
	PollAfterSeconds  int                    `json:"poll_after_seconds,omitempty"`
	Terminal          bool                   `json:"terminal"`
}

type capabilityAssetAuthorizer interface {
	Authorize(context.Context, content.DeliveryActor, backupasset.AssetRef, content.DeliveryAction) (content.AuthorizedAsset, error)
}

type capabilityCoordinator interface {
	RequestWork(context.Context, WorkRequest) (WorkResult, error)
	RemoveInterest(context.Context, string, InterestOwnerKind, string, InterestRemovedReason) error
}

type MalwareSafetyDecision struct {
	Active bool
	Safe   bool
	Status capabilityspec.ScanState
}

type MalwareSafetySource func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error)

type ActivePublicationIdentity struct {
	PipelineFingerprint    string
	SecurityPolicyRevision string
}

type PublicationIdentityTxSource func(
	context.Context,
	*gorm.DB,
	string,
	string,
) (ActivePublicationIdentity, error)

type CapabilityServiceDependencies struct {
	DB                     *gorm.DB
	Coordinator            capabilityCoordinator
	Authorize              capabilityAssetAuthorizer
	Now                    func() time.Time
	Enabled                func(context.Context) (bool, error)
	SecurityPolicyRevision func(context.Context) (string, error)
	ActivePipeline         func(context.Context, string, string) (string, error)
	PublicationIdentityTx  PublicationIdentityTxSource
	MalwareSafety          MalwareSafetySource
	PollAfterSeconds       int
}

type CapabilityService struct {
	db                     *gorm.DB
	coordinator            capabilityCoordinator
	authorize              capabilityAssetAuthorizer
	now                    func() time.Time
	enabled                func(context.Context) (bool, error)
	securityPolicyRevision func(context.Context) (string, error)
	activePipeline         func(context.Context, string, string) (string, error)
	publicationIdentityTx  PublicationIdentityTxSource
	malwareSafety          MalwareSafetySource
	pollAfterSeconds       int
}

func NewCapabilityService(dependencies CapabilityServiceDependencies) (*CapabilityService, error) {
	if dependencies.DB == nil || dependencies.Coordinator == nil || dependencies.Authorize == nil ||
		dependencies.Enabled == nil || dependencies.SecurityPolicyRevision == nil || dependencies.ActivePipeline == nil ||
		dependencies.PublicationIdentityTx == nil || dependencies.MalwareSafety == nil ||
		dependencies.PollAfterSeconds < 1 || dependencies.PollAfterSeconds > 30 {
		return nil, fmt.Errorf("%w: capability service dependencies are unavailable", ErrInvalidContract)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &CapabilityService{
		db: dependencies.DB, coordinator: dependencies.Coordinator, authorize: dependencies.Authorize,
		now: dependencies.Now, enabled: dependencies.Enabled,
		securityPolicyRevision: dependencies.SecurityPolicyRevision, activePipeline: dependencies.ActivePipeline,
		publicationIdentityTx: dependencies.PublicationIdentityTx, malwareSafety: dependencies.MalwareSafety,
		pollAfterSeconds: dependencies.PollAfterSeconds,
	}, nil
}

func (service *CapabilityService) RequestPreview(ctx context.Context, request PreviewJobRequest) (PreviewJobResult, error) {
	asset, profile, err := service.authorizeRequest(ctx, request.Actor, request.Ref, request.Representation, request.Profile)
	if err != nil {
		return PreviewJobResult{}, err
	}
	if asset.Provider == backupasset.ProviderCommand {
		return unsupportedPreviewResult(request.Representation, profile, ProcessingErrorUnsupportedFormat), nil
	}
	malware, err := service.currentMalwareSafety(ctx, asset)
	if err != nil {
		return PreviewJobResult{}, err
	}
	if !malware.Safe {
		return malwareBlockedPreviewResult(request.Representation, profile, malware), nil
	}
	if existing, ok, err := service.activeDerived(ctx, asset, request.Representation, profile); err != nil {
		return PreviewJobResult{}, err
	} else if ok {
		return existing, nil
	}
	advertisement, ok, err := service.readyCapability(ctx, profile)
	if err != nil {
		return PreviewJobResult{}, err
	}
	if !ok {
		return notDeployedPreviewResult(request.Representation, profile), nil
	}
	policyRevision, err := service.securityPolicyRevision(nonNilProcessingContext(ctx))
	if err != nil || policyRevision == "" || len(policyRevision) > 128 {
		return PreviewJobResult{}, fmt.Errorf("%w: security policy revision unavailable", ErrInvalidContract)
	}
	descriptor := WorkDescriptorV1{
		SchemaVersion: 1, Source: request.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		Capability:                 advertisement.Capability, CapabilitySchema: advertisement.CapabilitySchema,
		PipelineFingerprint: advertisement.PipelineFingerprint, OutputProfile: advertisement.OutputProfile,
		SecurityPolicyRevision: policyRevision, Parameters: CanonicalProductionParameters(profile),
	}
	if err := ValidateProductionWorkDescriptorV1(descriptor, false); err != nil {
		return PreviewJobResult{}, err
	}
	work, err := service.coordinator.RequestWork(nonNilProcessingContext(ctx), WorkRequest{
		Descriptor: descriptor,
		Interest:   InterestRequest{OwnerKind: InterestWorkspace, OwnerKey: processingOwnerKey(request.Actor.UserID), PriorityClass: PriorityInteractive, Priority: 1000},
	})
	if errors.Is(err, ErrNotDeployed) {
		return notDeployedPreviewResult(request.Representation, profile), nil
	}
	if err != nil {
		return PreviewJobResult{}, err
	}
	result, err := service.pollAuthorized(
		ctx, request.Actor, asset,
		PreviewJobLookup{Actor: request.Actor, Ref: request.Ref, JobID: work.InterestID},
		true,
	)
	if err != nil {
		compensationErr := service.compensatePreviewInterest(ctx, request.Actor, work)
		if compensationErr != nil {
			return PreviewJobResult{}, errors.Join(err, compensationErr)
		}
		return PreviewJobResult{}, err
	}
	if result.Terminal {
		result.JobID = ""
	}
	return result, nil
}

func (service *CapabilityService) PollPreview(ctx context.Context, lookup PreviewJobLookup) (PreviewJobResult, error) {
	asset, _, err := service.authorizeRequest(ctx, lookup.Actor, lookup.Ref, "", "")
	if err != nil {
		return PreviewJobResult{}, err
	}
	return service.pollAuthorized(ctx, lookup.Actor, asset, lookup, true)
}

func (service *CapabilityService) CancelPreview(ctx context.Context, lookup PreviewJobLookup) error {
	asset, _, err := service.authorizeRequest(ctx, lookup.Actor, lookup.Ref, "", "")
	if err != nil {
		return err
	}
	interest, job, err := service.loadInterest(ctx, lookup, processingOwnerKey(lookup.Actor.UserID))
	if err != nil || job.RecoveryPointID != asset.Ref.RecoveryPointID || job.EntryID != asset.Ref.EntryID {
		return ErrProcessingHandleNotFound
	}
	return service.coordinator.RemoveInterest(nonNilProcessingContext(ctx), interest.JobID, InterestWorkspace, interest.OwnerKey, InterestRemovedCanceled)
}

func (service *CapabilityService) State(ctx context.Context, request PreviewStateRequest) (AssetProcessingState, error) {
	asset, _, err := service.authorizeRequest(ctx, request.Actor, request.Ref, "", "")
	if err != nil {
		return AssetProcessingState{}, err
	}
	malware, err := service.currentMalwareSafety(ctx, asset)
	if err != nil {
		return AssetProcessingState{}, err
	}
	result := AssetProcessingState{SchemaVersion: 1, Representations: make([]PreviewJobResult, 0, 5)}
	for _, representation := range []PreviewRepresentation{
		PreviewThumbnail, PreviewText, PreviewDocumentPages, PreviewMedia, PreviewArchiveIndex,
	} {
		profile, supported := previewProfile(representation, "", asset.MediaType)
		if !supported || asset.Provider == backupasset.ProviderCommand {
			result.Representations = append(result.Representations,
				unsupportedPreviewResult(representation, profile, ProcessingErrorUnsupportedFormat))
			continue
		}
		if !malware.Safe {
			result.Representations = append(result.Representations, malwareBlockedPreviewResult(representation, profile, malware))
			continue
		}
		current, active, activeErr := service.activeDerived(ctx, asset, representation, profile)
		if activeErr != nil {
			return AssetProcessingState{}, activeErr
		}
		if active {
			result.Representations = append(result.Representations, current)
			continue
		}
		_, ready, readyErr := service.readyCapability(ctx, profile)
		if readyErr != nil {
			return AssetProcessingState{}, readyErr
		}
		if !ready {
			result.Representations = append(result.Representations, notDeployedPreviewResult(representation, profile))
			continue
		}
		result.Representations = append(result.Representations, nativeReadyPreviewResult(representation, profile))
	}
	return result, nil
}

func (service *CapabilityService) authorizeRequest(
	ctx context.Context,
	actor content.DeliveryActor,
	ref backupasset.AssetRef,
	representation PreviewRepresentation,
	requestedProfile string,
) (content.AuthorizedAsset, capabilityspec.Profile, error) {
	if service == nil || service.db == nil || service.authorize == nil || actor.UserID == 0 || backupasset.ValidateAssetRef(ref) != nil {
		return content.AuthorizedAsset{}, capabilityspec.Profile{}, ErrProcessingHandleNotFound
	}
	enabled, err := service.enabled(nonNilProcessingContext(ctx))
	if err != nil || !enabled {
		return content.AuthorizedAsset{}, capabilityspec.Profile{}, ErrProcessingDisabled
	}
	asset, err := service.authorize.Authorize(nonNilProcessingContext(ctx), actor, ref, content.DeliveryPreview)
	if err != nil {
		return content.AuthorizedAsset{}, capabilityspec.Profile{}, err
	}
	if asset.Ref != ref || backupasset.ValidateOpaqueID(asset.CatalogGenerationID) != nil ||
		asset.ProviderCapabilityRevision <= 0 || asset.SourceFingerprint == "" || len(asset.SourceFingerprint) > 128 ||
		len(asset.EntryFingerprint) > 128 || asset.Size < 0 {
		return content.AuthorizedAsset{}, capabilityspec.Profile{}, fmt.Errorf("%w: authorized processing asset is invalid", ErrInvalidContract)
	}
	if representation == "" {
		return asset, capabilityspec.Profile{}, nil
	}
	profile, ok := previewProfile(representation, requestedProfile, asset.MediaType)
	if !ok {
		return asset, capabilityspec.Profile{}, nil
	}
	return asset, profile, nil
}

func (service *CapabilityService) pollAuthorized(
	ctx context.Context,
	actor content.DeliveryActor,
	asset content.AuthorizedAsset,
	lookup PreviewJobLookup,
	consumeTerminal bool,
) (PreviewJobResult, error) {
	if backupasset.ValidateOpaqueID(lookup.JobID) != nil {
		return PreviewJobResult{}, ErrProcessingHandleNotFound
	}
	interest, job, err := service.loadInterest(ctx, lookup, processingOwnerKey(actor.UserID))
	if err != nil || job.RecoveryPointID != asset.Ref.RecoveryPointID || job.EntryID != asset.Ref.EntryID ||
		job.CatalogGenerationID != asset.CatalogGenerationID || job.SourceFingerprint != asset.SourceFingerprint {
		return PreviewJobResult{}, ErrProcessingHandleNotFound
	}
	malware, err := service.currentMalwareSafety(ctx, asset)
	if err != nil {
		return PreviewJobResult{}, err
	}
	if !malware.Safe {
		profile, ok := capabilityspec.Lookup(job.Capability, job.OutputProfile, false)
		if !ok || profile.CapabilitySchema != job.CapabilitySchema {
			return PreviewJobResult{}, ErrProcessingHandleNotFound
		}
		if !consumeTerminal {
			return malwareBlockedPreviewResult(representationForCapability(job.Capability), profile, malware), nil
		}
		malware, err = service.currentMalwareSafety(ctx, asset)
		if err != nil {
			return PreviewJobResult{}, err
		}
		if !malware.Safe {
			if isTerminalState(ProcessingState(job.State)) {
				if err := service.consumeTerminalInterest(ctx, interest, job); err != nil {
					return PreviewJobResult{}, err
				}
			} else if err := service.coordinator.RemoveInterest(
				nonNilProcessingContext(ctx), job.ID, InterestWorkspace, interest.OwnerKey, InterestRemovedCompleted,
			); err != nil {
				return PreviewJobResult{}, fmt.Errorf("consume malware-blocked processing interest: %w", err)
			}
			result := malwareBlockedPreviewResult(representationForCapability(job.Capability), profile, malware)
			result.JobID = interest.ID
			return result, nil
		}
	}
	result := previewResultForJob(job, interest.ID, service.pollAfterSeconds)
	var observedIdentity *ActivePublicationIdentity
	if ProcessingState(job.State) == ProcessingSucceeded {
		profile, ok := capabilityspec.Lookup(job.Capability, job.OutputProfile, false)
		if !ok || profile.CapabilitySchema != job.CapabilitySchema {
			result.State, result.Reason = ProcessingProductFailed, string(ProcessingErrorInvalidOutput)
		} else {
			rows, identity, queryErr := service.activeDerivedPublicationsDB(ctx, service.db, asset, profile, job.ID, false)
			if queryErr != nil {
				return PreviewJobResult{}, queryErr
			}
			observedIdentity = &identity
			if len(rows) != 1 {
				result.State, result.Reason = ProcessingProductFailed, string(ProcessingErrorInvalidOutput)
			} else if rows[0].Completeness == string(ArtifactPartial) {
				result.State, result.Coverage = ProcessingProductPartial, string(ArtifactPartial)
			} else {
				result.State, result.Coverage = ProcessingProductDerived, string(ArtifactComplete)
			}
		}
	}
	if consumeTerminal && isTerminalState(ProcessingState(job.State)) {
		return service.consumeTerminalProduct(ctx, asset, interest, job, observedIdentity)
	}
	return result, nil
}

func (service *CapabilityService) compensatePreviewInterest(
	ctx context.Context,
	actor content.DeliveryActor,
	work WorkResult,
) error {
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(nonNilProcessingContext(ctx)),
		previewInterestCompensationTimeout,
	)
	defer cancel()
	if err := service.coordinator.RemoveInterest(
		cleanupCtx, work.JobID, InterestWorkspace, processingOwnerKey(actor.UserID), InterestRemovedCanceled,
	); err != nil {
		return fmt.Errorf("compensate undisclosed processing interest: %w", err)
	}
	return nil
}

func (service *CapabilityService) currentMalwareSafety(
	ctx context.Context,
	asset content.AuthorizedAsset,
) (MalwareSafetyDecision, error) {
	decision, err := service.malwareSafety(nonNilProcessingContext(ctx), asset)
	if err != nil || !validMalwareSafetyDecision(decision) {
		return MalwareSafetyDecision{}, errors.Join(ErrInvalidContract, err)
	}
	return decision, nil
}

func (service *CapabilityService) loadInterest(
	ctx context.Context,
	lookup PreviewJobLookup,
	ownerKey string,
) (model.BackupAssetProcessingInterest, model.BackupAssetProcessingJob, error) {
	var interest model.BackupAssetProcessingInterest
	var job model.BackupAssetProcessingJob
	err := service.db.WithContext(nonNilProcessingContext(ctx)).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND owner_kind = ? AND owner_key = ? AND active = ?", lookup.JobID, InterestWorkspace, ownerKey, true,
		).Limit(1).Find(&interest)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProcessingHandleNotFound
		}
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", interest.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || job.RecoveryPointID != lookup.Ref.RecoveryPointID || job.EntryID != lookup.Ref.EntryID {
			return ErrProcessingHandleNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrProcessingHandleNotFound) {
			return interest, job, ErrProcessingHandleNotFound
		}
		return interest, job, fmt.Errorf("load processing interest: %w", err)
	}
	return interest, job, nil
}

func (service *CapabilityService) consumeTerminalInterest(
	ctx context.Context,
	loadedInterest model.BackupAssetProcessingInterest,
	loadedJob model.BackupAssetProcessingJob,
) error {
	err := service.db.WithContext(nonNilProcessingContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var interest model.BackupAssetProcessingInterest
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?",
			loadedInterest.ID, loadedJob.ID, loadedInterest.OwnerKind, loadedInterest.OwnerKey, true,
		).Limit(1).Find(&interest)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProcessingHandleNotFound
		}
		var job model.BackupAssetProcessingJob
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND state = ? AND transition_revision = ?", loadedJob.ID, loadedJob.State, loadedJob.TransitionRevision,
		).Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !isTerminalState(ProcessingState(job.State)) {
			return ErrProcessingHandleNotFound
		}
		now := service.now().UTC()
		updated := tx.Model(&model.BackupAssetProcessingInterest{}).Where("id = ? AND active = ?", interest.ID, true).
			Updates(map[string]any{"active": false, "removed_reason": string(InterestRemovedCompleted), "removed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProcessingHandleNotFound
		}
		return nil
	})
	if err == nil || errors.Is(err, ErrProcessingHandleNotFound) {
		return err
	}
	return fmt.Errorf("consume terminal processing interest: %w", err)
}

func (service *CapabilityService) consumeTerminalProduct(
	ctx context.Context,
	asset content.AuthorizedAsset,
	loadedInterest model.BackupAssetProcessingInterest,
	loadedJob model.BackupAssetProcessingJob,
	observedIdentity *ActivePublicationIdentity,
) (PreviewJobResult, error) {
	var product PreviewJobResult
	err := service.db.WithContext(nonNilProcessingContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var interest model.BackupAssetProcessingInterest
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND job_id = ? AND owner_kind = ? AND owner_key = ? AND active = ?",
			loadedInterest.ID, loadedJob.ID, loadedInterest.OwnerKind, loadedInterest.OwnerKey, true,
		).Limit(1).Find(&interest)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProcessingHandleNotFound
		}
		var job model.BackupAssetProcessingJob
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND state = ? AND transition_revision = ?", loadedJob.ID, loadedJob.State, loadedJob.TransitionRevision,
		).Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !isTerminalState(ProcessingState(job.State)) ||
			job.RecoveryPointID != asset.Ref.RecoveryPointID || job.CatalogGenerationID != asset.CatalogGenerationID ||
			job.EntryID != asset.Ref.EntryID || job.SourceFingerprint != asset.SourceFingerprint {
			return ErrProcessingHandleNotFound
		}
		product = previewResultForJob(job, interest.ID, service.pollAfterSeconds)
		if ProcessingState(job.State) == ProcessingSucceeded {
			profile, ok := capabilityspec.Lookup(job.Capability, job.OutputProfile, false)
			if !ok || profile.CapabilitySchema != job.CapabilitySchema {
				product.State, product.Reason = ProcessingProductFailed, string(ProcessingErrorInvalidOutput)
			} else {
				if observedIdentity == nil {
					return fmt.Errorf("%w: terminal publication identity was not observed", ErrInvalidContract)
				}
				rows, currentIdentity, queryErr := service.activeDerivedPublicationsDB(ctx, tx, asset, profile, job.ID, true)
				if queryErr != nil {
					return queryErr
				}
				if currentIdentity != *observedIdentity {
					return ErrProcessingPublicationIdentityChanged
				}
				applyTerminalPublicationResult(&product, rows)
			}
		}
		now := service.now().UTC()
		updated := tx.Model(&model.BackupAssetProcessingInterest{}).Where("id = ? AND active = ?", interest.ID, true).
			Updates(map[string]any{"active": false, "removed_reason": string(InterestRemovedCompleted), "removed_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProcessingHandleNotFound
		}
		return nil
	})
	if err == nil || errors.Is(err, ErrProcessingHandleNotFound) {
		return product, err
	}
	return PreviewJobResult{}, fmt.Errorf("consume terminal processing product: %w", err)
}

func (service *CapabilityService) activeDerived(
	ctx context.Context,
	asset content.AuthorizedAsset,
	representation PreviewRepresentation,
	profile capabilityspec.Profile,
) (PreviewJobResult, bool, error) {
	if profile.Capability == "" {
		return unsupportedPreviewResult(representation, profile, ProcessingErrorUnsupportedFormat), true, nil
	}
	rows, err := service.activeDerivedPublications(ctx, asset, profile, "")
	if err != nil {
		return PreviewJobResult{}, false, err
	}
	if len(rows) > 1 {
		return PreviewJobResult{}, false, backupasset.ErrConflict
	}
	if len(rows) == 0 {
		return PreviewJobResult{}, false, nil
	}
	row := rows[0]
	if backupasset.ValidateOpaqueID(row.SetID) != nil ||
		(row.Completeness != string(ArtifactComplete) && row.Completeness != string(ArtifactPartial)) {
		return PreviewJobResult{}, false, nil
	}
	state := ProcessingProductDerived
	if row.Completeness == string(ArtifactPartial) {
		state = ProcessingProductPartial
	}
	return PreviewJobResult{
		SchemaVersion: 1, State: state, Representation: representation,
		Capability: profile.Capability, Profile: profile.OutputProfile, Coverage: row.Completeness,
		Freshness: "current", FallbackActions: processingFallbackActions(), Terminal: true,
	}, true, nil
}

type activeDerivedPublication struct {
	SetID        string
	Completeness string
}

func (service *CapabilityService) activeDerivedPublications(
	ctx context.Context,
	asset content.AuthorizedAsset,
	profile capabilityspec.Profile,
	jobID string,
) ([]activeDerivedPublication, error) {
	rows, _, err := service.activeDerivedPublicationsDB(ctx, service.db, asset, profile, jobID, false)
	return rows, err
}

func (service *CapabilityService) activeDerivedPublicationsDB(
	ctx context.Context,
	db *gorm.DB,
	asset content.AuthorizedAsset,
	profile capabilityspec.Profile,
	jobID string,
	lock bool,
) ([]activeDerivedPublication, ActivePublicationIdentity, error) {
	if db == nil {
		return nil, ActivePublicationIdentity{}, fmt.Errorf("%w: processing publication database unavailable", ErrInvalidContract)
	}
	identity, err := service.activePublicationIdentity(ctx, db, profile, lock)
	if err != nil {
		return nil, ActivePublicationIdentity{}, err
	}
	if identity.PipelineFingerprint == "" {
		return nil, identity, nil
	}
	var rows []activeDerivedPublication
	query := db.WithContext(nonNilProcessingContext(ctx)).Table("backup_asset_derived_artifact_sets AS sets").
		Select("sets.id AS set_id, sets.completeness").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Joins(`JOIN backup_asset_processing_attempts AS attempts
			ON attempts.id = sets.attempt_id AND attempts.job_id = jobs.id`).
		Where(`sets.recovery_point_id = ? AND sets.catalog_generation_id = ? AND sets.entry_id = ?
			AND sets.source_fingerprint = ? AND sets.security_policy_revision = ? AND sets.state = ?
			AND sets.work_key = jobs.work_key
			AND ((sets.projection_required = ? AND sets.projection_published = ? AND sets.projection_revision = 0)
				OR (sets.projection_required = ? AND sets.projection_published = ? AND sets.projection_revision > 0))
			AND jobs.recovery_point_id = ? AND jobs.catalog_generation_id = ? AND jobs.entry_id = ?
			AND jobs.source_fingerprint = ? AND jobs.entry_fingerprint = ?
			AND jobs.provider_capability_revision = ? AND jobs.security_policy_revision = ?
			AND jobs.capability = ? AND jobs.capability_schema = ? AND jobs.output_profile = ?
			AND jobs.pipeline_fingerprint = ? AND jobs.state = ? AND jobs.is_current = ?
			AND jobs.finished_at IS NOT NULL AND jobs.current_artifact_set_id = sets.id
			AND jobs.current_attempt_id = sets.attempt_id AND attempts.state = ?
			AND attempts.is_current = ? AND attempts.finished_at IS NOT NULL`,
			asset.Ref.RecoveryPointID, asset.CatalogGenerationID, asset.Ref.EntryID, asset.SourceFingerprint,
			identity.SecurityPolicyRevision, "active", false, false, true, true,
			asset.Ref.RecoveryPointID, asset.CatalogGenerationID, asset.Ref.EntryID,
			asset.SourceFingerprint, asset.EntryFingerprint, asset.ProviderCapabilityRevision, identity.SecurityPolicyRevision,
			profile.Capability, profile.CapabilitySchema, profile.OutputProfile, identity.PipelineFingerprint,
			ProcessingSucceeded, false, "succeeded", false)
	if jobID != "" {
		query = query.Where("jobs.id = ?", jobID)
	}
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	query = query.Order("sets.updated_at DESC, sets.id ASC").Limit(2).Scan(&rows)
	if query.Error != nil {
		return nil, ActivePublicationIdentity{}, fmt.Errorf("load active Derived representation: %w", query.Error)
	}
	return rows, identity, nil
}

func (service *CapabilityService) activePublicationIdentity(
	ctx context.Context,
	db *gorm.DB,
	profile capabilityspec.Profile,
	transactional bool,
) (ActivePublicationIdentity, error) {
	ctx = nonNilProcessingContext(ctx)
	if transactional {
		identity, err := service.publicationIdentityTx(ctx, db, profile.Capability, profile.OutputProfile)
		if err != nil || !validActivePublicationIdentity(identity) {
			return ActivePublicationIdentity{}, errors.Join(ErrInvalidContract, err)
		}
		return identity, nil
	}
	activePipeline, err := service.activePipeline(ctx, profile.Capability, profile.OutputProfile)
	if err != nil {
		return ActivePublicationIdentity{}, fmt.Errorf("load active processing pipeline: %w", err)
	}
	if activePipeline == "" {
		return ActivePublicationIdentity{}, nil
	}
	policyRevision, err := service.securityPolicyRevision(ctx)
	identity := ActivePublicationIdentity{
		PipelineFingerprint: activePipeline, SecurityPolicyRevision: policyRevision,
	}
	if err != nil || !validActivePublicationIdentity(identity) {
		return ActivePublicationIdentity{}, errors.Join(ErrInvalidContract, err)
	}
	return identity, nil
}

func validActivePublicationIdentity(identity ActivePublicationIdentity) bool {
	if identity.PipelineFingerprint == "" {
		return identity.SecurityPolicyRevision == ""
	}
	return len(identity.PipelineFingerprint) <= 128 && identity.SecurityPolicyRevision != "" &&
		len(identity.SecurityPolicyRevision) <= 128
}

func applyTerminalPublicationResult(result *PreviewJobResult, rows []activeDerivedPublication) {
	if result == nil {
		return
	}
	if len(rows) != 1 || backupasset.ValidateOpaqueID(rows[0].SetID) != nil {
		result.State, result.Reason, result.Coverage = ProcessingProductFailed, string(ProcessingErrorInvalidOutput), ""
		return
	}
	switch rows[0].Completeness {
	case string(ArtifactPartial):
		result.State, result.Reason, result.Coverage = ProcessingProductPartial, "", string(ArtifactPartial)
	case string(ArtifactComplete):
		result.State, result.Reason, result.Coverage = ProcessingProductDerived, "", string(ArtifactComplete)
	default:
		result.State, result.Reason, result.Coverage = ProcessingProductFailed, string(ProcessingErrorInvalidOutput), ""
	}
}

func (service *CapabilityService) ArchiveExtractCapability(
	ctx context.Context,
) (CapabilityAdvertisement, error) {
	if service == nil || service.db == nil {
		return CapabilityAdvertisement{}, ErrNotDeployed
	}
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityArchiveExtractEntry,
		capabilityspec.ProfileArchiveMemberV1,
		false,
	)
	if !ok {
		return CapabilityAdvertisement{}, ErrNotDeployed
	}
	advertisement, ready, err := service.readyCapability(nonNilProcessingContext(ctx), profile)
	if err != nil {
		return CapabilityAdvertisement{}, err
	}
	if !ready {
		return CapabilityAdvertisement{}, ErrNotDeployed
	}
	return advertisement, nil
}

func (service *CapabilityService) readyCapability(ctx context.Context, profile capabilityspec.Profile) (CapabilityAdvertisement, bool, error) {
	if profile.Capability == "" {
		return CapabilityAdvertisement{}, false, nil
	}
	activePipeline, err := service.activePipeline(nonNilProcessingContext(ctx), profile.Capability, profile.OutputProfile)
	if err != nil {
		return CapabilityAdvertisement{}, false, fmt.Errorf("load active processing pipeline: %w", err)
	}
	if activePipeline == "" {
		return CapabilityAdvertisement{}, false, nil
	}
	var row model.BackupAssetWorkerCapability
	result := service.db.WithContext(nonNilProcessingContext(ctx)).Table("backup_asset_worker_capabilities AS capabilities").
		Select("capabilities.*").
		Joins("JOIN backup_asset_worker_identities AS workers ON workers.id = capabilities.worker_id").
		Where(`workers.trust_state = ? AND workers.health_state = ? AND capabilities.health_state = ?
			AND capabilities.capability = ? AND capabilities.capability_schema = ? AND capabilities.output_profile = ?
			AND capabilities.pipeline_fingerprint = ?`,
			"active", "ready", "ready", profile.Capability, profile.CapabilitySchema, profile.OutputProfile, activePipeline).
		Order("workers.last_seen_at DESC").Limit(1).Scan(&row)
	if result.Error != nil {
		return CapabilityAdvertisement{}, false, fmt.Errorf("load ready processing capability: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return CapabilityAdvertisement{}, false, nil
	}
	return CapabilityAdvertisement{
		SchemaVersion: 1, Capability: row.Capability, CapabilitySchema: row.CapabilitySchema,
		PipelineFingerprint: row.PipelineFingerprint, OutputProfile: row.OutputProfile,
	}, true, nil
}

func previewProfile(representation PreviewRepresentation, requested, mediaType string) (capabilityspec.Profile, bool) {
	candidates := map[PreviewRepresentation][]capabilityspec.Identity{
		PreviewThumbnail: {{Capability: capabilityspec.CapabilityImageThumbnail, OutputProfile: capabilityspec.ProfileRasterThumbnailV1}},
		PreviewText: {
			{Capability: capabilityspec.CapabilityTextExtract, OutputProfile: capabilityspec.ProfileBoundedTextV1},
			{Capability: capabilityspec.CapabilityImageOCR, OutputProfile: capabilityspec.ProfileTesseractTextV1},
		},
		PreviewDocumentPages: {{Capability: capabilityspec.CapabilityDocumentConvert, OutputProfile: capabilityspec.ProfileStaticPagesV1}},
		PreviewMedia:         {{Capability: capabilityspec.CapabilityMediaTranscode, OutputProfile: capabilityspec.ProfileBrowserPreviewV1}},
		PreviewArchiveIndex:  {{Capability: capabilityspec.CapabilityArchiveInspect, OutputProfile: capabilityspec.ProfileArchiveIndexV1}},
	}
	for _, candidate := range candidates[representation] {
		if requested != "" && requested != candidate.OutputProfile {
			continue
		}
		profile, ok := capabilityspec.Lookup(candidate.Capability, candidate.OutputProfile, false)
		if ok && profile.ValidateMedia(mediaType, mediaType) == nil {
			return profile, true
		}
	}
	return capabilityspec.Profile{}, false
}

func canonicalPreviewParameters(profile capabilityspec.Profile) CanonicalParametersV1 {
	return CanonicalProductionParameters(profile)
}

// CanonicalProductionParameters returns the closed, output-affecting
// parameters shared by public preview work and internal continuation work.
func CanonicalProductionParameters(profile capabilityspec.Profile) CanonicalParametersV1 {
	codec := "noop"
	switch profile.Capability {
	case capabilityspec.CapabilityImageThumbnail:
		codec = "webp"
	case capabilityspec.CapabilityTextExtract, capabilityspec.CapabilityImageOCR,
		capabilityspec.CapabilityArchiveInspect, capabilityspec.CapabilitySecretClassify:
		codec = "text"
	case capabilityspec.CapabilityDocumentConvert:
		codec = "pdf"
	case capabilityspec.CapabilityMediaTranscode:
		codec = "mp4"
	}
	pageEnd := int(min(profile.Limits.MaxPages, 30))
	if pageEnd < 1 {
		pageEnd = 1
	}
	return CanonicalParametersV1{
		SchemaVersion: 1, Width: 1280, Height: 720, Codec: codec, PageStart: 1, PageEnd: pageEnd,
		Quality: 80, Language: "und", Model: "builtin-v1", FontProfile: "safe-v1",
		MemberStart: 0, MemberEnd: 0, FrameStart: 0, FrameEnd: 0, TimeStartMillis: 0, TimeEndMillis: 0,
		Orientation: "auto", CropX: 0, CropY: 0, CropWidth: 1280, CropHeight: 720,
		MaxPages: profile.Limits.MaxPages, MaxPixels: profile.Limits.MaxPixels,
		MaxDurationMillis: profile.Limits.MaxDurationMillis, MaxExpandedBytes: profile.Limits.MaxExpandedBytes,
		MaxOutputBytes: profile.Limits.MaxOutputBytes, MaxOutputCount: profile.Limits.MaxOutputCount,
		TruncationPolicy: "partial", RequiresMaterialization: profile.RequiresMaterialization,
	}
}

func previewResultForJob(job model.BackupAssetProcessingJob, interestID string, pollAfter int) PreviewJobResult {
	representation := representationForCapability(job.Capability)
	result := PreviewJobResult{
		SchemaVersion: 1, JobID: interestID, State: ProcessingProductQueued, Representation: representation,
		Capability: job.Capability, Profile: job.OutputProfile, Freshness: "current",
		FallbackActions: processingFallbackActions(), PollAfterSeconds: pollAfter,
	}
	switch ProcessingState(job.State) {
	case ProcessingSucceeded:
		result.State, result.Terminal, result.PollAfterSeconds = ProcessingProductDerived, true, 0
	case ProcessingFailed, ProcessingCanceled, ProcessingExpired, ProcessingSuperseded:
		result.State, result.Terminal, result.PollAfterSeconds = ProcessingProductFailed, true, 0
		result.Reason = job.ErrorCode
		if category, err := ProcessingErrorCode(job.ErrorCode).Category(); err == nil {
			result.Retryable = category == TransientError
		}
	}
	return result
}

func representationForCapability(capability string) PreviewRepresentation {
	switch capability {
	case capabilityspec.CapabilityImageThumbnail:
		return PreviewThumbnail
	case capabilityspec.CapabilityTextExtract, capabilityspec.CapabilityImageOCR:
		return PreviewText
	case capabilityspec.CapabilityDocumentConvert:
		return PreviewDocumentPages
	case capabilityspec.CapabilityMediaTranscode:
		return PreviewMedia
	case capabilityspec.CapabilityArchiveInspect:
		return PreviewArchiveIndex
	default:
		return ""
	}
}

func notDeployedPreviewResult(representation PreviewRepresentation, profile capabilityspec.Profile) PreviewJobResult {
	return PreviewJobResult{
		SchemaVersion: 1, State: ProcessingProductNotDeployed, Representation: representation,
		Capability: profile.Capability, Profile: profile.OutputProfile,
		Reason: string(ProcessingErrorWorkerUnavailable), FallbackActions: processingFallbackActions(), Terminal: true,
	}
}

func malwareBlockedPreviewResult(
	representation PreviewRepresentation,
	profile capabilityspec.Profile,
	decision MalwareSafetyDecision,
) PreviewJobResult {
	if !decision.Active {
		result := notDeployedPreviewResult(representation, profile)
		result.ScanStatus = string(capabilityspec.ScanNotScanned)
		return result
	}
	return PreviewJobResult{
		SchemaVersion: 1, State: ProcessingProductUnsupported, Representation: representation,
		Capability: profile.Capability, Profile: profile.OutputProfile, ScanStatus: string(decision.Status),
		Reason: string(ProcessingErrorInvalidOutput), FallbackActions: processingFallbackActions(), Terminal: true,
	}
}

func validMalwareSafetyDecision(value MalwareSafetyDecision) bool {
	switch value.Status {
	case capabilityspec.ScanNotScanned, capabilityspec.ScanNoFinding, capabilityspec.ScanFinding, capabilityspec.ScanStale:
	default:
		return false
	}
	if value.Safe {
		return value.Active && value.Status == capabilityspec.ScanNoFinding
	}
	return !value.Active && value.Status == capabilityspec.ScanNotScanned || value.Active
}

func unsupportedPreviewResult(representation PreviewRepresentation, profile capabilityspec.Profile, reason ProcessingErrorCode) PreviewJobResult {
	return PreviewJobResult{
		SchemaVersion: 1, State: ProcessingProductUnsupported, Representation: representation,
		Capability: profile.Capability, Profile: profile.OutputProfile, Reason: string(reason),
		FallbackActions: processingFallbackActions(), Terminal: true,
	}
}

func nativeReadyPreviewResult(representation PreviewRepresentation, profile capabilityspec.Profile) PreviewJobResult {
	return PreviewJobResult{
		SchemaVersion: 1, State: ProcessingProductNative, Representation: representation,
		Capability: profile.Capability, Profile: profile.OutputProfile, Freshness: "current",
		FallbackActions: processingFallbackActions(), Terminal: true,
	}
}

func processingFallbackActions() []string {
	return []string{"native_preview", "download"}
}

func processingOwnerKey(userID uint) string {
	digest := sha256.Sum256([]byte("xirang.processing.workspace-owner.v1\x00" + strconv.FormatUint(uint64(userID), 10)))
	return "workspace:" + hex.EncodeToString(digest[:])
}

func nonNilProcessingContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
