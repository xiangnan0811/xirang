package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	processingupdater "xirang/backend/internal/backupasset/processing/updater"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const processingSecurityPolicyRevision = "security-policy-v1"

const secretContinuationBatchSize = 100

const malwareResultMaxBytes = 64 << 10

type processingRuntimeDependencies struct {
	DB               *gorm.DB
	Foundation       *backupasset.FoundationService
	Settings         *settings.Service
	Keyring          *backupasset.Keyring
	Lease            *backupasset.LeaseService
	Source           content.SourceResolver
	Authorize        processingAssetAuthorizer
	ValidateRoot     func(context.Context, string) error
	RevalidateSource processing.ProcessingSourceRevalidator
	Projection       processing.DerivedProjectionPort
	Metrics          processing.Metrics
	Now              func() time.Time
}

type processingAssetAuthorizer interface {
	Authorize(context.Context, content.DeliveryActor, backupasset.AssetRef, content.DeliveryAction) (content.AuthorizedAsset, error)
}

type ProcessingWorkerCounts struct {
	Active      int64 `json:"active"`
	Draining    int64 `json:"draining"`
	Degraded    int64 `json:"degraded"`
	Quarantined int64 `json:"quarantined"`
}

type ProcessingSlotSummary struct {
	InteractiveUsed  int64 `json:"interactive_used"`
	InteractiveTotal int   `json:"interactive_total"`
	BackgroundUsed   int64 `json:"background_used"`
	BackgroundTotal  int   `json:"background_total"`
}

type ProcessingQueueSummary struct {
	Total               int64            `json:"total"`
	ByState             map[string]int64 `json:"by_state"`
	ByPriority          map[string]int64 `json:"by_priority"`
	OldestQueuedSeconds int64            `json:"oldest_queued_seconds"`
}

type ProcessingOutcomeSummary struct {
	ByErrorCategory map[string]int64 `json:"by_error_category"`
}

type ProcessingDerivedSummary struct {
	ByState       map[string]int64 `json:"by_state"`
	LogicalBytes  int64            `json:"logical_bytes"`
	PhysicalBytes int64            `json:"physical_bytes"`
	OrphanBytes   int64            `json:"orphan_bytes"`
	QuotaBytes    int64            `json:"quota_bytes"`
}

type ProcessingUpdaterCandidate struct {
	CandidateID           string                              `json:"candidate_id"`
	SourceKind            string                              `json:"source_kind"`
	SourceID              string                              `json:"source_id"`
	Version               string                              `json:"version"`
	ManifestDigest        string                              `json:"manifest_digest"`
	SigningKeyFingerprint string                              `json:"signing_key_fingerprint"`
	BundleFingerprint     string                              `json:"bundle_fingerprint"`
	State                 string                              `json:"state"`
	Reason                string                              `json:"reason,omitempty"`
	VerifiedAt            *time.Time                          `json:"verified_at,omitempty"`
	ActivatedAt           *time.Time                          `json:"activated_at,omitempty"`
	CapabilityChanges     []ProcessingUpdaterCapabilityChange `json:"capability_changes"`
}

type ProcessingUpdaterCapabilityChange struct {
	Capability       string   `json:"capability"`
	CapabilitySchema string   `json:"capability_schema"`
	Profiles         []string `json:"profiles"`
}

type ProcessingUpdaterStatus struct {
	SchemaVersion int                         `json:"schema_version"`
	Enabled       bool                        `json:"enabled"`
	OnlineEnabled bool                        `json:"online_enabled"`
	Active        *ProcessingUpdaterCandidate `json:"active,omitempty"`
}

type ProcessingBackfillPolicy struct {
	SchemaVersion         int    `json:"schema_version"`
	Revision              string `json:"revision"`
	Paused                bool   `json:"paused"`
	BatchSize             int    `json:"batch_size"`
	JobsPerHour           int    `json:"jobs_per_hour"`
	BytesPerHour          int64  `json:"bytes_per_hour"`
	ProviderConcurrency   int    `json:"provider_concurrency"`
	CapabilityConcurrency int    `json:"capability_concurrency"`
}

type ProcessingBackfillPolicyUpdate struct {
	ExpectedRevision      string
	Paused                bool
	BatchSize             int
	JobsPerHour           int
	BytesPerHour          int64
	ProviderConcurrency   int
	CapabilityConcurrency int
}

type ProcessingUpdaterActivationRequest struct {
	CandidateID               string
	ExpectedActiveFingerprint *string
}

type processingUpdaterImpact struct {
	affectContent bool
	affectOCR     bool
}

// ProcessingAdminSummary deliberately contains only bounded aggregates. It is
// safe for the internal Admin adapter and must never grow identity, source,
// path, grant, fence, certificate, or raw-error fields.
type ProcessingAdminSummary struct {
	SchemaVersion  int                      `json:"schema_version"`
	Configured     bool                     `json:"configured"`
	LocalEnabled   bool                     `json:"local_enabled"`
	RemoteEnabled  bool                     `json:"remote_enabled"`
	BackfillPolicy ProcessingBackfillPolicy `json:"backfill_policy"`
	Workers        ProcessingWorkerCounts   `json:"worker_counts"`
	Slots          ProcessingSlotSummary    `json:"slots"`
	Queue          ProcessingQueueSummary   `json:"queue"`
	Outcomes       ProcessingOutcomeSummary `json:"outcomes"`
	Derived        ProcessingDerivedSummary `json:"derived"`
	ReconciledAt   *time.Time               `json:"reconciled_at"`
}

type managedProcessingRuntime struct {
	db               *gorm.DB
	foundation       *backupasset.FoundationService
	settings         *settings.Service
	keyring          *backupasset.Keyring
	lease            *backupasset.LeaseService
	source           content.SourceResolver
	authorize        processingAssetAuthorizer
	validateRoot     func(context.Context, string) error
	revalidateSource processing.ProcessingSourceRevalidator
	projection       processing.DerivedProjectionPort
	metrics          processing.Metrics
	now              func() time.Time

	coordinator       *processing.Coordinator
	grants            *processing.GrantService
	attemptBroker     *content.AttemptBroker
	store             *processing.DerivedStore
	lifecycle         *processing.DerivedLifecycle
	sink              *processing.ArtifactSink
	reconciler        *processing.Reconciler
	derivedReconciler *processing.DerivedReconciler
	invalidation      processingPipelineInvalidator
	malwareEvidence   processingMalwareEvidenceReader
	malwareWork       processingMalwareWorkRequester
	protocol          *processing.ProtocolService
	workerProtocol    *processing.WorkerProtocolService
	capabilityService *processing.CapabilityService
	coverageService   *processing.CoverageService

	mu                       sync.RWMutex
	config                   backupasset.ProcessingConfig
	lastReconciled           *time.Time
	runCancel                context.CancelFunc
	runDone                  chan struct{}
	ready                    atomic.Bool
	stopped                  atomic.Bool
	updaterMu                sync.Mutex
	updaterScanRequested     bool
	pendingUpdaterActivation *ProcessingUpdaterActivationRequest
	lastUpdaterActivation    time.Time
	updaterCandidateImpacts  map[string]processingUpdaterImpact
	updaterCandidateChanges  map[string][]ProcessingUpdaterCapabilityChange
}

type processingPipelineInvalidator interface {
	Invalidate(context.Context, processing.InvalidationRequest) (processing.InvalidationResult, error)
}

type processingMalwareEvidenceReader interface {
	ReadAuthorized(context.Context, processing.DerivedArtifactAuthorization, io.Writer) error
}

type processingMalwareWorkRequester interface {
	RequestWork(context.Context, processing.WorkRequest) (processing.WorkResult, error)
}

type processingMalwareEvidence struct {
	ArtifactID    string
	PlaintextSize int64
}

type secretContinuationCandidate struct {
	ArtifactID                 string
	PlaintextDigest            string
	RecoveryPointID            string
	CatalogGenerationID        string
	EntryID                    string
	SourceFingerprint          string
	ProviderCapabilityRevision int64
}

func newProcessingRuntime(dependencies processingRuntimeDependencies) (*managedProcessingRuntime, error) {
	if dependencies.DB == nil || dependencies.Foundation == nil || dependencies.Keyring == nil || dependencies.Lease == nil ||
		dependencies.Source == nil || dependencies.Authorize == nil || dependencies.ValidateRoot == nil ||
		dependencies.RevalidateSource == nil || dependencies.Projection == nil {
		return nil, fmt.Errorf("%w: Processing runtime dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = processing.NoopMetrics{}
	}
	return &managedProcessingRuntime{
		db: dependencies.DB, foundation: dependencies.Foundation, settings: dependencies.Settings, keyring: dependencies.Keyring,
		lease: dependencies.Lease, source: dependencies.Source, authorize: dependencies.Authorize, validateRoot: dependencies.ValidateRoot,
		revalidateSource: dependencies.RevalidateSource, projection: dependencies.Projection, metrics: dependencies.Metrics, now: dependencies.Now,
	}, nil
}

func (runtime *managedProcessingRuntime) Startup(ctx context.Context) error {
	if runtime == nil || runtime.foundation == nil {
		return fmt.Errorf("%w: Processing runtime unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := runtime.foundation.ProcessingConfig()
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	runtime.config = config
	runtime.mu.Unlock()
	if !config.Enabled {
		runtime.ready.Store(false)
		return nil
	}
	if !config.LocalWorker.Enabled && !config.RemoteWorker.Enabled {
		if err := runtime.initializeControlPlane(config); err != nil {
			return err
		}
		runtime.ready.Store(false)
		return nil
	}
	if err := runtime.initialize(ctx, config); err != nil {
		runtime.ready.Store(false)
		return err
	}
	if err := runtime.reconcile(ctx); err != nil {
		runtime.ready.Store(false)
		return err
	}
	runtime.ready.Store(true)
	return nil
}

func (runtime *managedProcessingRuntime) initializeControlPlane(config backupasset.ProcessingConfig) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.capabilityService != nil && runtime.coverageService != nil && runtime.coordinator != nil {
		return nil
	}
	coordinator, capabilityService, coverageService, err := runtime.buildControlPlane(config)
	if err != nil {
		return err
	}
	runtime.coordinator = coordinator
	runtime.malwareWork = coordinator
	runtime.capabilityService = capabilityService
	runtime.coverageService = coverageService
	return nil
}

func (runtime *managedProcessingRuntime) buildControlPlane(
	config backupasset.ProcessingConfig,
) (*processing.Coordinator, *processing.CapabilityService, *processing.CoverageService, error) {
	coordinator, err := processing.NewCoordinator(runtime.db, runtime.lease, runtime.now, processing.CoordinatorConfig{
		QueueMax: config.QueueMax, InteractiveReservedSlots: config.InteractiveSlots,
		BackgroundSlots: config.BackgroundSlots, PullLease: config.PullLease,
		AttemptTimeout: config.AttemptTimeout, RetryMax: config.RetryMax,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	capabilityService, err := processing.NewCapabilityService(processing.CapabilityServiceDependencies{
		DB: runtime.db, Coordinator: coordinator, Authorize: runtime.authorize, Now: runtime.now,
		Enabled: func(context.Context) (bool, error) {
			enabled, enabledErr := runtime.foundation.FeatureEnabled()
			current, configErr := runtime.foundation.ProcessingConfig()
			return enabled && current.Enabled, errors.Join(enabledErr, configErr)
		},
		SecurityPolicyRevision: func(context.Context) (string, error) { return processingSecurityPolicyRevision, nil },
		ActivePipeline:         runtime.activePipelineFingerprint,
		PublicationIdentityTx:  runtime.activePublicationIdentityTx,
		MalwareSafety:          runtime.malwareSafetyForAsset,
		PollAfterSeconds:       2,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	coverageService, err := processing.NewCoverageService(runtime.db, runtime.now)
	if err != nil {
		return nil, nil, nil, err
	}
	return coordinator, capabilityService, coverageService, nil
}

func (runtime *managedProcessingRuntime) malwareSafetyForAsset(
	ctx context.Context,
	asset content.AuthorizedAsset,
) (processing.MalwareSafetyDecision, error) {
	decision := processing.MalwareSafetyDecision{
		Active: false,
		Safe:   false,
		Status: capabilityspec.ScanNotScanned,
	}
	if runtime == nil || runtime.db == nil || backupasset.ValidateAssetRef(asset.Ref) != nil ||
		backupasset.ValidateOpaqueID(asset.CatalogGenerationID) != nil ||
		asset.ProviderCapabilityRevision <= 0 || asset.SourceFingerprint == "" ||
		len(asset.SourceFingerprint) > 128 || len(asset.EntryFingerprint) > 128 || asset.Size < 0 {
		return decision, processing.ErrInvalidContract
	}
	ctx = nonNilRuntimeContext(ctx)
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityMalwareScan,
		capabilityspec.ProfileSignatureScanV1,
		false,
	)
	if !ok {
		return decision, processing.ErrInvalidContract
	}
	pipeline, err := runtime.activePipelineFingerprint(ctx, profile.Capability, profile.OutputProfile)
	if err != nil {
		return decision, err
	}
	if pipeline == "" {
		return decision, nil
	}
	decision.Active = true
	bundleFingerprint, err := runtime.activeUpdaterFingerprint(ctx)
	if err != nil || bundleFingerprint == "" {
		return decision, errors.Join(processing.ErrInvalidContract, err)
	}

	evidence, found, err := runtime.currentMalwareEvidence(ctx, asset, profile, pipeline)
	if err != nil {
		return decision, err
	}
	if !found {
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	}
	if runtime.malwareEvidence == nil {
		decision.Status = capabilityspec.ScanStale
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	}

	var payload bytes.Buffer
	payload.Grow(int(evidence.PlaintextSize))
	err = runtime.malwareEvidence.ReadAuthorized(ctx, processing.DerivedArtifactAuthorization{
		ArtifactID: evidence.ArtifactID, RecoveryPointID: asset.Ref.RecoveryPointID,
		CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint,
	}, &payload)
	if err != nil {
		if ctx.Err() != nil {
			return decision, ctx.Err()
		}
		decision.Status = capabilityspec.ScanStale
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	}
	result, err := processing.DecodeCanonicalMalwareResult(payload.Bytes())
	if err != nil || result.SignatureBundleFingerprint != bundleFingerprint ||
		result.Completeness != capabilityspec.CoverageComplete || result.ScannedBytes != asset.Size {
		decision.Status = capabilityspec.ScanStale
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	}
	decision.Status = result.Result
	switch result.Result {
	case capabilityspec.ScanNoFinding:
		decision.Safe = true
		return decision, nil
	case capabilityspec.ScanFinding:
		return decision, nil
	case capabilityspec.ScanNotScanned, capabilityspec.ScanStale:
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	default:
		decision.Status = capabilityspec.ScanStale
		return decision, runtime.requestMalwareContinuation(ctx, asset, profile, pipeline)
	}
}

func (runtime *managedProcessingRuntime) currentMalwareEvidence(
	ctx context.Context,
	asset content.AuthorizedAsset,
	profile capabilityspec.Profile,
	pipeline string,
) (processingMalwareEvidence, bool, error) {
	var rows []processingMalwareEvidence
	query := runtime.db.WithContext(ctx).
		Table("backup_asset_processing_jobs AS jobs").
		Select("artifacts.id AS artifact_id, artifacts.plaintext_size AS plaintext_size").
		Joins(`JOIN backup_asset_derived_artifact_sets AS artifact_sets
			ON artifact_sets.id = jobs.current_artifact_set_id AND artifact_sets.job_id = jobs.id`).
		Joins(`JOIN backup_asset_derived_artifacts AS artifacts
				ON artifacts.artifact_set_id = artifact_sets.id`).
		Joins(`JOIN backup_asset_processing_attempts AS attempts
				ON attempts.id = artifact_sets.attempt_id AND attempts.job_id = jobs.id`).
		Where(`jobs.recovery_point_id = ? AND jobs.catalog_generation_id = ? AND jobs.entry_id = ?
			AND jobs.source_fingerprint = ? AND jobs.entry_fingerprint = ?
			AND jobs.provider_capability_revision = ? AND jobs.capability = ?
			AND jobs.capability_schema = ? AND jobs.pipeline_fingerprint = ?
			AND jobs.output_profile = ? AND jobs.security_policy_revision = ?
				AND jobs.state = ? AND jobs.is_current = ? AND jobs.finished_at IS NOT NULL
				AND jobs.current_attempt_id = artifact_sets.attempt_id
				AND attempts.state = ? AND attempts.is_current = ? AND attempts.finished_at IS NOT NULL`,
			asset.Ref.RecoveryPointID, asset.CatalogGenerationID, asset.Ref.EntryID,
			asset.SourceFingerprint, asset.EntryFingerprint, asset.ProviderCapabilityRevision,
			profile.Capability, profile.CapabilitySchema, pipeline, profile.OutputProfile,
			processingSecurityPolicyRevision, processing.ProcessingSucceeded, false, "succeeded", false).
		Where(`artifact_sets.recovery_point_id = ? AND artifact_sets.catalog_generation_id = ?
				AND artifact_sets.entry_id = ? AND artifact_sets.source_fingerprint = ?
				AND artifact_sets.security_policy_revision = ? AND artifact_sets.state = ?
				AND artifact_sets.completeness = ?
				AND artifact_sets.artifact_count = ? AND artifact_sets.projection_required = ?
			AND artifacts.ordinal = ? AND artifacts.role = ? AND artifacts.media_type = ?
			AND artifacts.completeness = ? AND artifacts.plaintext_size > 0
			AND artifacts.plaintext_size <= ?`,
			asset.Ref.RecoveryPointID, asset.CatalogGenerationID, asset.Ref.EntryID,
			asset.SourceFingerprint, processingSecurityPolicyRevision,
			"active", processing.ArtifactComplete, 1, false, 0,
			processing.ArtifactRoleMetadata, "application/json", processing.ArtifactComplete,
			malwareResultMaxBytes).
		Order("jobs.updated_at DESC, jobs.id ASC").Limit(2).Find(&rows)
	if query.Error != nil {
		return processingMalwareEvidence{}, false, fmt.Errorf("load current malware evidence: %w", query.Error)
	}
	if len(rows) > 1 {
		return processingMalwareEvidence{}, false, backupasset.ErrConflict
	}
	if len(rows) == 0 {
		return processingMalwareEvidence{}, false, nil
	}
	return rows[0], true, nil
}

func (runtime *managedProcessingRuntime) requestMalwareContinuation(
	ctx context.Context,
	asset content.AuthorizedAsset,
	profile capabilityspec.Profile,
	pipeline string,
) error {
	if runtime.malwareWork == nil {
		return fmt.Errorf("%w: malware continuation coordinator unavailable", processing.ErrInvalidContract)
	}
	descriptor := processing.WorkDescriptorV1{
		SchemaVersion: 1, Source: asset.Ref, CatalogGenerationID: asset.CatalogGenerationID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		Capability:                 profile.Capability, CapabilitySchema: profile.CapabilitySchema,
		PipelineFingerprint: pipeline, OutputProfile: profile.OutputProfile,
		SecurityPolicyRevision: processingSecurityPolicyRevision,
		Parameters:             processing.CanonicalProductionParameters(profile),
	}
	if err := processing.ValidateProductionWorkDescriptorV1(descriptor, false); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte("xirang.processing.malware-continuation.v1\x00" +
		asset.Ref.RecoveryPointID + "\x00" + asset.Ref.EntryID))
	_, err := runtime.malwareWork.RequestWork(ctx, processing.WorkRequest{
		Descriptor: descriptor,
		Interest: processing.InterestRequest{
			OwnerKind: processing.InterestSystem, OwnerKey: "malware:" + hex.EncodeToString(digest[:]),
			PriorityClass: processing.PriorityBackground, Priority: 900,
		},
	})
	if errors.Is(err, processing.ErrNotDeployed) {
		return nil
	}
	return err
}

func (runtime *managedProcessingRuntime) scheduleSecretContinuations(ctx context.Context) (int, error) {
	if runtime == nil || runtime.db == nil {
		return 0, fmt.Errorf("%w: secret classification continuation unavailable", backupasset.ErrInvalidState)
	}
	ctx = nonNilRuntimeContext(ctx)
	runtime.mu.RLock()
	enabled := runtime.config.SecretClassify
	coordinator := runtime.coordinator
	runtime.mu.RUnlock()
	if runtime.foundation != nil {
		config, err := runtime.foundation.ProcessingConfig()
		if err != nil {
			return 0, err
		}
		enabled = config.SecretClassify
		runtime.mu.Lock()
		runtime.config = config
		coordinator = runtime.coordinator
		runtime.mu.Unlock()
	}
	if !enabled {
		return 0, nil
	}
	if coordinator == nil {
		return 0, fmt.Errorf("%w: secret classification coordinator unavailable", backupasset.ErrInvalidState)
	}
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilitySecretClassify,
		capabilityspec.ProfileBoundedSecretV1,
		true,
	)
	if !ok {
		return 0, processing.ErrInvalidContract
	}

	var advertisement model.BackupAssetWorkerCapability
	ready := runtime.db.WithContext(ctx).
		Table("backup_asset_worker_capabilities AS capabilities").
		Select("capabilities.*").
		Joins("JOIN backup_asset_worker_identities AS workers ON workers.id = capabilities.worker_id").
		Where(`workers.trust_state = ? AND workers.health_state = ? AND capabilities.health_state = ?
			AND capabilities.capability = ? AND capabilities.capability_schema = ? AND capabilities.output_profile = ?`,
			"active", "ready", "ready", profile.Capability, profile.CapabilitySchema, profile.OutputProfile).
		Order("capabilities.pipeline_fingerprint ASC, capabilities.id ASC").Limit(1).Find(&advertisement)
	if ready.Error != nil {
		return 0, fmt.Errorf("load secret classification capability: %w", ready.Error)
	}
	if ready.RowsAffected != 1 {
		return 0, nil
	}

	var candidates []secretContinuationCandidate
	query := runtime.db.WithContext(ctx).
		Table("backup_asset_derived_artifacts AS artifacts").
		Select(`artifacts.id AS artifact_id, artifacts.plaintext_digest,
			sets.recovery_point_id, sets.catalog_generation_id, sets.entry_id,
			sets.source_fingerprint, jobs.provider_capability_revision`).
		Joins("JOIN backup_asset_derived_artifact_sets AS sets ON sets.id = artifacts.artifact_set_id").
		Joins("JOIN backup_asset_processing_jobs AS jobs ON jobs.id = sets.job_id").
		Joins(`JOIN backup_asset_derived_blob_references AS refs ON refs.artifact_id = artifacts.id
			AND refs.recovery_point_id = sets.recovery_point_id
			AND refs.catalog_generation_id = sets.catalog_generation_id
			AND refs.entry_id = sets.entry_id
			AND refs.source_fingerprint = sets.source_fingerprint`).
		Joins("JOIN backup_asset_derived_blobs AS blobs ON blobs.id = artifacts.blob_id").
		Where(`sets.state = ? AND sets.completeness = ?
			AND sets.projection_required = ? AND sets.projection_published = ?
			AND sets.security_policy_revision = ?
			AND artifacts.media_type = ? AND artifacts.completeness = ?
			AND refs.state = ? AND blobs.state = ?
			AND jobs.state = ? AND jobs.source_fingerprint = sets.source_fingerprint
			AND jobs.security_policy_revision = sets.security_policy_revision
			AND ((jobs.capability = ? AND artifacts.role = ?) OR (jobs.capability = ? AND artifacts.role = ?))`,
			"active", processing.ArtifactComplete, true, true, processingSecurityPolicyRevision,
			"text/plain", processing.ArtifactComplete, "active", "active", processing.ProcessingSucceeded,
			capabilityspec.CapabilityTextExtract, processing.ArtifactRoleContent,
			capabilityspec.CapabilityImageOCR, processing.ArtifactRoleOCR).
		Where(`NOT EXISTS (
			SELECT 1 FROM backup_asset_processing_jobs AS classifications
			WHERE classifications.recovery_point_id = sets.recovery_point_id
				AND classifications.catalog_generation_id = sets.catalog_generation_id
				AND classifications.entry_id = sets.entry_id
				AND classifications.source_fingerprint = sets.source_fingerprint
				AND classifications.entry_fingerprint = artifacts.plaintext_digest
				AND classifications.capability = ?
				AND classifications.capability_schema = ?
				AND classifications.pipeline_fingerprint = ?
				AND classifications.output_profile = ?
				AND classifications.security_policy_revision = ?
				AND (classifications.is_current = ? OR classifications.state = ?)
		)`, profile.Capability, profile.CapabilitySchema, advertisement.PipelineFingerprint,
			profile.OutputProfile, processingSecurityPolicyRevision, true, processing.ProcessingSucceeded).
		Order("artifacts.id ASC").Limit(secretContinuationBatchSize).Scan(&candidates)
	if query.Error != nil {
		return 0, fmt.Errorf("load secret classification continuations: %w", query.Error)
	}

	scheduled := 0
	for _, candidate := range candidates {
		descriptor := processing.WorkDescriptorV1{
			SchemaVersion: 1,
			Source: backupasset.AssetRef{
				RecoveryPointID: candidate.RecoveryPointID,
				EntryID:         candidate.EntryID,
			},
			CatalogGenerationID:        candidate.CatalogGenerationID,
			SourceFingerprint:          candidate.SourceFingerprint,
			EntryFingerprint:           candidate.PlaintextDigest,
			ProviderCapabilityRevision: candidate.ProviderCapabilityRevision,
			Capability:                 profile.Capability,
			CapabilitySchema:           profile.CapabilitySchema,
			PipelineFingerprint:        advertisement.PipelineFingerprint,
			OutputProfile:              profile.OutputProfile,
			SecurityPolicyRevision:     processingSecurityPolicyRevision,
			Parameters:                 processing.CanonicalProductionParameters(profile),
		}
		if err := processing.ValidateProductionWorkDescriptorV1(descriptor, true); err != nil {
			return scheduled, err
		}
		result, err := coordinator.RequestWork(ctx, processing.WorkRequest{
			Descriptor: descriptor,
			Interest: processing.InterestRequest{
				OwnerKind:     processing.InterestSystem,
				OwnerKey:      "secret-classify:" + candidate.ArtifactID,
				PriorityClass: processing.PriorityBackground,
				Priority:      500,
			},
		})
		if errors.Is(err, processing.ErrNotDeployed) {
			return scheduled, nil
		}
		if err != nil {
			return scheduled, err
		}
		if result.Created {
			scheduled++
		}
	}
	return scheduled, nil
}

func (runtime *managedProcessingRuntime) initialize(ctx context.Context, config backupasset.ProcessingConfig) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.workerProtocol != nil {
		return nil
	}
	if err := runtime.validateRoot(ctx, config.DerivedStore.Root); err != nil {
		return fmt.Errorf("validate Derived Store root: %w", err)
	}
	if _, err := runtime.keyring.RewrapDomains(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		return runtime.invalidateUnreadableDerivedKey(ctx, config, err)
	}
	if _, err := runtime.keyring.Ensure(ctx, backupasset.KeyDomainDerivedStore); err != nil {
		if errors.Is(err, backupasset.ErrKeyLost) || errors.Is(err, backupasset.ErrKeyUnavailable) {
			return runtime.invalidateUnreadableDerivedKey(ctx, config, err)
		}
		return fmt.Errorf("ensure Derived Store key: %w", err)
	}
	coordinator, capabilityService, coverageService, err := runtime.buildControlPlane(config)
	if err != nil {
		return err
	}
	grants, err := processing.NewGrantService(runtime.db, runtime.lease, runtime.now, processing.GrantConfig{
		TTL: config.AttemptTimeout,
		InputLimits: processing.GrantLimits{
			MaxRequests: config.Input.MaxRequests, MaxBytesPerRequest: config.Input.RequestMaxBytes,
			MaxCumulativeBytes: config.Input.CumulativeMaxBytes, MaxInFlight: config.Input.MaxInFlight,
		},
		SinkLimits: processing.GrantLimits{
			MaxRequests: int64(config.Sink.MaxArtifacts), MaxBytesPerRequest: config.Sink.ArtifactMaxBytes,
			MaxCumulativeBytes: config.Sink.TotalMaxBytes, MaxInFlight: int64(config.Sink.MaxArtifacts),
		},
	})
	if err != nil {
		return err
	}
	attemptBroker, err := content.NewAttemptBroker(runtime.source, grants, runtime.now)
	if err != nil {
		return err
	}
	store, err := processing.NewDerivedStore(ctx, runtime.db, runtime.keyring, processing.DerivedStoreConfig{
		Root: config.DerivedStore.Root, ChunkSize: config.DerivedStore.ChunkBytes,
		BlobMaxBytes: config.DerivedStore.BlobMaxBytes, GlobalMaxBytes: config.DerivedStore.GlobalMaxBytes,
		ValidateRoot: runtime.validateRoot,
	}, runtime.now)
	if err != nil {
		return err
	}
	lifecycle, err := processing.NewDerivedLifecycle(runtime.db, store, runtime.projection, runtime.now, runtime.lease)
	if err != nil {
		return err
	}
	invalidation, err := processing.NewInvalidationController(runtime.db, coordinator, lifecycle, runtime.now)
	if err != nil {
		return err
	}
	sink, err := processing.NewArtifactSink(
		runtime.db, runtime.lease, grants, store, lifecycle, runtime.revalidateSource,
		func(context.Context) (string, error) { return processingSecurityPolicyRevision, nil },
		runtime.activePipelineFingerprint,
		runtime.now, processing.ArtifactSinkConfig{
			MaxArtifacts: config.Sink.MaxArtifacts, MaxArtifactBytes: config.Sink.ArtifactMaxBytes,
			MaxTotalBytes: config.Sink.TotalMaxBytes,
		},
	)
	if err != nil {
		return err
	}
	sink.SetMetrics(runtime.metrics)
	reconciler, err := processing.NewReconciler(coordinator, grants, runtime.now, processing.ReconcilerConfig{
		BatchSize: config.DerivedStore.ReconcileBatchSize, RetryBase: config.RetryBase,
	})
	if err != nil {
		return err
	}
	derivedReconciler, err := processing.NewDerivedReconciler(store, lifecycle, config.DerivedStore.ReconcileBatchSize)
	if err != nil {
		return err
	}
	registry, err := runtime.activeProductionCapabilityRegistry(ctx)
	if err != nil {
		return err
	}
	protocol, err := processing.NewProtocolService(runtime.db, registry, runtime.now)
	if err != nil {
		return err
	}
	workerProtocol, err := processing.NewWorkerProtocolService(protocol, coordinator, grants, attemptBroker, sink)
	if err != nil {
		return err
	}
	runtime.coordinator = coordinator
	runtime.grants = grants
	runtime.attemptBroker = attemptBroker
	runtime.store = store
	runtime.lifecycle = lifecycle
	runtime.malwareEvidence = lifecycle
	runtime.malwareWork = coordinator
	runtime.sink = sink
	runtime.reconciler = reconciler
	runtime.derivedReconciler = derivedReconciler
	runtime.invalidation = invalidation
	runtime.protocol = protocol
	runtime.workerProtocol = workerProtocol
	runtime.capabilityService = capabilityService
	runtime.coverageService = coverageService
	return nil
}

func (runtime *managedProcessingRuntime) activeProductionCapabilityRegistry(
	ctx context.Context,
) (*processing.CapabilityRegistry, error) {
	fingerprint, err := runtime.activeUpdaterFingerprint(ctx)
	if err != nil {
		return nil, err
	}
	if fingerprint == "" {
		return processing.NewCapabilityRegistry(nil)
	}
	return productionCapabilityRegistryForFingerprint(fingerprint)
}

func (runtime *managedProcessingRuntime) activePipelineFingerprint(
	ctx context.Context,
	capability string,
	outputProfile string,
) (string, error) {
	if runtime == nil || capability == "" || outputProfile == "" {
		return "", processing.ErrProtocolUnavailable
	}
	fingerprint, err := runtime.activeUpdaterFingerprint(nonNilRuntimeContext(ctx))
	if err != nil {
		return "", err
	}
	if fingerprint == "" {
		return "", nil
	}
	registry, err := productionCapabilityRegistryForFingerprint(fingerprint)
	if err != nil {
		return "", err
	}
	pipeline, ok := registry.ActivePipelineFingerprint(capability, outputProfile)
	if !ok {
		return "", nil
	}
	return pipeline, nil
}

func (runtime *managedProcessingRuntime) activePublicationIdentityTx(
	ctx context.Context,
	tx *gorm.DB,
	capability string,
	outputProfile string,
) (processing.ActivePublicationIdentity, error) {
	if runtime == nil || tx == nil || capability == "" || outputProfile == "" {
		return processing.ActivePublicationIdentity{}, processing.ErrProtocolUnavailable
	}
	fingerprint, err := activeUpdaterFingerprintDB(nonNilRuntimeContext(ctx), tx, true)
	if err != nil {
		return processing.ActivePublicationIdentity{}, err
	}
	if fingerprint == "" {
		return processing.ActivePublicationIdentity{}, nil
	}
	registry, err := productionCapabilityRegistryForFingerprint(fingerprint)
	if err != nil {
		return processing.ActivePublicationIdentity{}, err
	}
	pipeline, ok := registry.ActivePipelineFingerprint(capability, outputProfile)
	if !ok {
		return processing.ActivePublicationIdentity{}, nil
	}
	return processing.ActivePublicationIdentity{
		PipelineFingerprint: pipeline, SecurityPolicyRevision: processingSecurityPolicyRevision,
	}, nil
}

func (runtime *managedProcessingRuntime) reconcileActivePipelines(ctx context.Context) error {
	if runtime == nil {
		return processing.ErrProtocolUnavailable
	}
	runtime.mu.RLock()
	controller := runtime.invalidation
	batchSize := runtime.config.Backfill.BatchSize
	runtime.mu.RUnlock()
	if controller == nil {
		return nil
	}
	if batchSize <= 0 {
		config, err := runtime.ProcessingConfig()
		if err != nil {
			return err
		}
		batchSize = config.Backfill.BatchSize
	}
	if batchSize <= 0 || batchSize > 10000 {
		return processing.ErrInvalidContract
	}
	fingerprint, err := runtime.activeUpdaterFingerprint(nonNilRuntimeContext(ctx))
	if err != nil {
		return err
	}
	if fingerprint == "" {
		return nil
	}
	registry, err := productionCapabilityRegistryForFingerprint(fingerprint)
	if err != nil {
		return err
	}
	targets := make([]processing.InvalidationTarget, 0, len(capabilityspec.WorkerProfiles()))
	seen := make(map[string]bool, len(capabilityspec.WorkerProfiles()))
	for _, profile := range capabilityspec.WorkerProfiles() {
		key := profile.Capability + "\x00" + profile.OutputProfile
		if seen[key] {
			continue
		}
		pipeline, ok := registry.ActivePipelineFingerprint(profile.Capability, profile.OutputProfile)
		if !ok {
			return processing.ErrProtocolUnavailable
		}
		seen[key] = true
		targets = append(targets, processing.InvalidationTarget{
			Capability: profile.Capability, OutputProfile: profile.OutputProfile,
			ActivePipelineFingerprint: pipeline,
		})
	}
	processing.SortInvalidationTargets(targets)
	_, err = controller.Invalidate(nonNilRuntimeContext(ctx), processing.InvalidationRequest{
		Targets: targets, BatchSize: batchSize, RequeuePriority: 900,
	})
	return err
}

func productionCapabilityRegistryForFingerprint(fingerprint string) (*processing.CapabilityRegistry, error) {
	bundles := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		bundles[profile.Capability] = []string{fingerprint}
	}
	return processing.NewProductionCapabilityRegistryWithBundles(bundles)
}

func (runtime *managedProcessingRuntime) replacementWorkerRegistry(
	fingerprint string,
) (*processing.CapabilityRegistry, error) {
	runtime.mu.RLock()
	configured := runtime.workerProtocol != nil
	runtime.mu.RUnlock()
	if !configured {
		return nil, nil
	}
	registry, err := productionCapabilityRegistryForFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return registry, nil
}

func (runtime *managedProcessingRuntime) invalidateUnreadableDerivedKey(
	ctx context.Context,
	config backupasset.ProcessingConfig,
	cause error,
) error {
	var key model.WrappedDomainKey
	result := runtime.db.WithContext(ctx).
		Where("domain = ? AND state IN ?", backupasset.KeyDomainDerivedStore, []string{
			string(backupasset.DomainKeyActive), string(backupasset.DomainKeyLost),
		}).
		Order("version DESC").Limit(1).Find(&key)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, result.Error)
	}
	store, err := processing.NewDerivedStore(ctx, runtime.db, runtime.keyring, processing.DerivedStoreConfig{
		Root: config.DerivedStore.Root, ChunkSize: config.DerivedStore.ChunkBytes,
		BlobMaxBytes: config.DerivedStore.BlobMaxBytes, GlobalMaxBytes: config.DerivedStore.GlobalMaxBytes,
		ValidateRoot: runtime.validateRoot,
	}, runtime.now)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	lifecycle, err := processing.NewDerivedLifecycle(runtime.db, store, runtime.projection, runtime.now, runtime.lease)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	if err := lifecycle.MarkActiveKeyLost(ctx, key.Version, config.DerivedStore.ReconcileBatchSize); err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	derivedReconciler, err := processing.NewDerivedReconciler(store, lifecycle, config.DerivedStore.ReconcileBatchSize)
	if err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	if _, err := derivedReconciler.Reconcile(ctx); err != nil {
		return errors.Join(processing.ErrDerivedStoreUnavailable, cause, err)
	}
	runtime.store = store
	runtime.lifecycle = lifecycle
	runtime.derivedReconciler = derivedReconciler
	return errors.Join(processing.ErrDerivedStoreUnavailable, cause)
}

func (runtime *managedProcessingRuntime) WorkerProtocol() *processing.WorkerProtocolService {
	if runtime == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.workerProtocol
}

func (runtime *managedProcessingRuntime) archiveMemberCoordinator() *processing.Coordinator {
	if runtime == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if !runtime.ready.Load() || runtime.stopped.Load() {
		return nil
	}
	return runtime.coordinator
}

func (runtime *managedProcessingRuntime) ReadDerivedArtifact(
	ctx context.Context,
	request content.DerivedArtifactRead,
	destination io.Writer,
) error {
	if runtime == nil || destination == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return processing.ErrDerivedUnauthorized
	}
	runtime.mu.RLock()
	lifecycle := runtime.lifecycle
	runtime.mu.RUnlock()
	if lifecycle == nil {
		return processing.ErrDerivedUnauthorized
	}
	return lifecycle.ReadAuthorized(ctx, processing.DerivedArtifactAuthorization{
		ArtifactID: request.ArtifactID, RecoveryPointID: request.RecoveryPointID,
		CatalogGenerationID: request.CatalogGenerationID, EntryID: request.EntryID,
		SourceFingerprint: request.SourceFingerprint,
	}, destination)
}

func (runtime *managedProcessingRuntime) RequestPreview(
	ctx context.Context,
	request processing.PreviewJobRequest,
) (processing.PreviewJobResult, error) {
	service := runtime.currentCapabilityService()
	if service == nil {
		return processing.PreviewJobResult{}, processing.ErrProcessingDisabled
	}
	return service.RequestPreview(ctx, request)
}

func (runtime *managedProcessingRuntime) PollPreview(
	ctx context.Context,
	lookup processing.PreviewJobLookup,
) (processing.PreviewJobResult, error) {
	service := runtime.currentCapabilityService()
	if service == nil {
		return processing.PreviewJobResult{}, processing.ErrProcessingDisabled
	}
	return service.PollPreview(ctx, lookup)
}

func (runtime *managedProcessingRuntime) CancelPreview(ctx context.Context, lookup processing.PreviewJobLookup) error {
	service := runtime.currentCapabilityService()
	if service == nil {
		return processing.ErrProcessingDisabled
	}
	return service.CancelPreview(ctx, lookup)
}

func (runtime *managedProcessingRuntime) ProcessingState(
	ctx context.Context,
	request processing.PreviewStateRequest,
) (processing.AssetProcessingState, error) {
	service := runtime.currentCapabilityService()
	if service == nil {
		return processing.AssetProcessingState{}, processing.ErrProcessingDisabled
	}
	return service.State(ctx, request)
}

func (runtime *managedProcessingRuntime) ProcessingCoverage(ctx context.Context) (processing.CoverageSummary, error) {
	runtime.mu.RLock()
	service := runtime.coverageService
	runtime.mu.RUnlock()
	if service == nil || !runtime.processingEnabled() {
		return processing.CoverageSummary{}, processing.ErrProcessingDisabled
	}
	return service.Summary(ctx)
}

func (runtime *managedProcessingRuntime) ProcessingCapabilities(
	ctx context.Context,
) ([]processing.CapabilityInventoryItem, error) {
	runtime.mu.RLock()
	service := runtime.coverageService
	runtime.mu.RUnlock()
	if service == nil || !runtime.processingEnabled() {
		return nil, processing.ErrProcessingDisabled
	}
	config, err := runtime.foundation.ProcessingConfig()
	if err != nil {
		return nil, err
	}
	return service.Capabilities(ctx, config.SecretClassify)
}

func (runtime *managedProcessingRuntime) ProcessingUpdaterStatus(ctx context.Context) (ProcessingUpdaterStatus, error) {
	config, err := runtime.ProcessingConfig()
	if err != nil || !config.Enabled {
		return ProcessingUpdaterStatus{}, processing.ErrProcessingDisabled
	}
	result := ProcessingUpdaterStatus{SchemaVersion: 1, Enabled: config.Updater.Enabled, OnlineEnabled: config.Updater.OnlineEnabled}
	var row model.BackupAssetUpdaterMetadata
	query := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where("state = ?", "active").Order("updated_at DESC").Limit(1).Find(&row)
	if query.Error != nil {
		return ProcessingUpdaterStatus{}, fmt.Errorf("load active updater metadata: %w", query.Error)
	}
	if query.RowsAffected == 1 {
		candidate := processingUpdaterCandidate(row, runtime.processingUpdaterChanges(row.ID))
		result.Active = &candidate
	}
	return result, nil
}

func (runtime *managedProcessingRuntime) ProcessingUpdaterCandidates(ctx context.Context) ([]ProcessingUpdaterCandidate, error) {
	config, err := runtime.ProcessingConfig()
	if err != nil || !config.Enabled {
		return nil, processing.ErrProcessingDisabled
	}
	var rows []model.BackupAssetUpdaterMetadata
	if err := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where(
		"source_kind = ? AND state IN ?", "admin_registered", []string{"verified", "active", "failed"},
	).Order("updated_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load updater candidates: %w", err)
	}
	result := make([]ProcessingUpdaterCandidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, processingUpdaterCandidate(row, runtime.processingUpdaterChanges(row.ID)))
	}
	return result, nil
}

func (runtime *managedProcessingRuntime) ProcessingBackfillPolicy() (ProcessingBackfillPolicy, error) {
	config, err := runtime.ProcessingConfig()
	if err != nil || !config.Enabled {
		return ProcessingBackfillPolicy{}, processing.ErrProcessingDisabled
	}
	return newProcessingBackfillPolicy(config.Backfill)
}

func (runtime *managedProcessingRuntime) UpdateProcessingBackfillPolicy(
	ctx context.Context,
	request ProcessingBackfillPolicyUpdate,
) (ProcessingBackfillPolicy, error) {
	if runtime == nil || runtime.settings == nil || len(request.ExpectedRevision) != 64 {
		return ProcessingBackfillPolicy{}, processing.ErrInvalidContract
	}
	updates := map[string]string{
		"backup_assets.processing_backfill_paused":                 strconv.FormatBool(request.Paused),
		"backup_assets.processing_backfill_batch_size":             strconv.Itoa(request.BatchSize),
		"backup_assets.processing_backfill_jobs_per_hour":          strconv.Itoa(request.JobsPerHour),
		"backup_assets.processing_backfill_bytes_per_hour":         strconv.FormatInt(request.BytesPerHour, 10),
		"backup_assets.processing_backfill_provider_concurrency":   strconv.Itoa(request.ProviderConcurrency),
		"backup_assets.processing_backfill_capability_concurrency": strconv.Itoa(request.CapabilityConcurrency),
	}
	for key, value := range updates {
		if err := runtime.settings.Validate(key, value); err != nil {
			return ProcessingBackfillPolicy{}, processing.ErrInvalidContract
		}
	}
	err := runtime.settings.WithBackupAssetMutation(nonNilRuntimeContext(ctx), func(current map[string]string) error {
		policy, parseErr := processingBackfillPolicyFromValues(current)
		if parseErr != nil {
			return parseErr
		}
		if policy.Revision != request.ExpectedRevision {
			return backupasset.ErrConflict
		}
		if err := runtime.settings.ValidateBackupAssetEffectiveUpdate(current, updates); err != nil {
			return processing.ErrInvalidContract
		}
		return runtime.settings.UpdateMany(updates)
	})
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	return runtime.ProcessingBackfillPolicy()
}

func (runtime *managedProcessingRuntime) RequestProcessingUpdaterScan(context.Context) error {
	config, err := runtime.ProcessingConfig()
	if err != nil || !config.Enabled || !config.Updater.Enabled {
		return processing.ErrProcessingDisabled
	}
	runtime.updaterMu.Lock()
	runtime.updaterScanRequested = true
	runtime.updaterMu.Unlock()
	return nil
}

func (runtime *managedProcessingRuntime) ActivateProcessingUpdaterCandidate(
	ctx context.Context,
	request ProcessingUpdaterActivationRequest,
) error {
	if backupasset.ValidateOpaqueID(request.CandidateID) != nil {
		return processing.ErrInvalidContract
	}
	config, err := runtime.ProcessingConfig()
	if err != nil || !config.Enabled || !config.Updater.Enabled {
		return processing.ErrProcessingDisabled
	}
	var candidate model.BackupAssetUpdaterMetadata
	query := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where("id = ? AND state = ?", request.CandidateID, "verified").Limit(1).Find(&candidate)
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected != 1 {
		return backupasset.ErrNotFound
	}
	status, err := runtime.ProcessingUpdaterStatus(ctx)
	if err != nil {
		return err
	}
	if status.Active == nil && request.ExpectedActiveFingerprint != nil || status.Active != nil &&
		(request.ExpectedActiveFingerprint == nil || *request.ExpectedActiveFingerprint != status.Active.BundleFingerprint) {
		return backupasset.ErrConflict
	}
	now := runtime.now().UTC()
	runtime.updaterMu.Lock()
	defer runtime.updaterMu.Unlock()
	lastAdmission, err := runtime.lastPersistentUpdaterAdmission(ctx)
	if err != nil {
		return err
	}
	if runtime.pendingUpdaterActivation != nil ||
		!runtime.lastUpdaterActivation.IsZero() && now.Before(runtime.lastUpdaterActivation.Add(time.Hour)) ||
		!lastAdmission.IsZero() && now.Before(lastAdmission.Add(time.Hour)) {
		return backupasset.ErrConflict
	}
	admittedAt := now
	if !admittedAt.After(candidate.CreatedAt) {
		admittedAt = candidate.CreatedAt.Add(time.Nanosecond)
	}
	updated := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Model(&model.BackupAssetUpdaterMetadata{}).
		Where("id = ? AND state = ? AND updated_at = ?", candidate.ID, "verified", candidate.UpdatedAt).
		Update("updated_at", admittedAt)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return backupasset.ErrConflict
	}
	copy := request
	if request.ExpectedActiveFingerprint != nil {
		value := *request.ExpectedActiveFingerprint
		copy.ExpectedActiveFingerprint = &value
	}
	runtime.pendingUpdaterActivation = &copy
	runtime.lastUpdaterActivation = admittedAt
	return nil
}

func (runtime *managedProcessingRuntime) RegisterUpdaterCandidate(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.RegisterCandidateRequest,
) (processingupdater.RegisterCandidateResult, error) {
	if !validRuntimeUpdaterIdentity(identity) {
		return processingupdater.RegisterCandidateResult{}, processingupdater.ErrUpdaterUnauthenticated
	}
	if !runtime.updaterEnabled() {
		return processingupdater.RegisterCandidateResult{}, processing.ErrProcessingDisabled
	}
	if !validRegisterUpdaterRequest(request) {
		return processingupdater.RegisterCandidateResult{}, processingupdater.ErrProtocolInvalid
	}
	receipt := request.Receipt
	impact, changes, err := processingUpdaterDeclarationForReceipt(receipt)
	if err != nil {
		return processingupdater.RegisterCandidateResult{}, err
	}
	var candidateID string
	err = runtime.db.WithContext(nonNilRuntimeContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var existing model.BackupAssetUpdaterMetadata
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"source_kind = ? AND source_id = ? AND version = ?", receipt.SourceKind, receipt.SourceID, receipt.Version,
		).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 1 {
			if existing.ManifestDigest != receipt.ManifestDigest || existing.SigningKeyFingerprint != receipt.SigningKeyFingerprint ||
				existing.BundleFingerprint != receipt.BundleFingerprint ||
				(existing.State != "verified" && existing.State != "active") {
				return backupasset.ErrConflict
			}
			candidateID = existing.ID
			return nil
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := runtime.now().UTC()
		verified := receipt.VerifiedAt.UTC()
		row := model.BackupAssetUpdaterMetadata{
			ID: id, SourceKind: receipt.SourceKind, SourceID: receipt.SourceID, Version: receipt.Version,
			ManifestDigest: receipt.ManifestDigest, SigningKeyFingerprint: receipt.SigningKeyFingerprint,
			BundleFingerprint: receipt.BundleFingerprint, State: "verified", VerifiedAt: &verified,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		candidateID = id
		return nil
	})
	if err != nil {
		return processingupdater.RegisterCandidateResult{}, err
	}
	runtime.updaterMu.Lock()
	if runtime.updaterCandidateImpacts == nil {
		runtime.updaterCandidateImpacts = make(map[string]processingUpdaterImpact)
	}
	runtime.updaterCandidateImpacts[candidateID] = impact
	if runtime.updaterCandidateChanges == nil {
		runtime.updaterCandidateChanges = make(map[string][]ProcessingUpdaterCapabilityChange)
	}
	runtime.updaterCandidateChanges[candidateID] = cloneProcessingUpdaterChanges(changes)
	runtime.updaterMu.Unlock()
	return processingupdater.RegisterCandidateResult{SchemaVersion: 1, CandidateID: candidateID}, nil
}

func (runtime *managedProcessingRuntime) PullUpdaterActivation(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.PullActivationRequest,
) (processingupdater.PullActivationResult, error) {
	if !validRuntimeUpdaterIdentity(identity) {
		return processingupdater.PullActivationResult{}, processingupdater.ErrUpdaterUnauthenticated
	}
	if !runtime.updaterEnabled() {
		return processingupdater.PullActivationResult{}, processing.ErrProcessingDisabled
	}
	if !validPullUpdaterRequest(request) {
		return processingupdater.PullActivationResult{}, processingupdater.ErrProtocolInvalid
	}
	active, err := runtime.activeUpdaterFingerprint(ctx)
	if err != nil {
		return processingupdater.PullActivationResult{}, err
	}
	if active != request.ActiveFingerprint {
		return processingupdater.PullActivationResult{}, backupasset.ErrConflict
	}
	result := processingupdater.PullActivationResult{SchemaVersion: 1, RetryAfterSeconds: 5}
	runtime.updaterMu.Lock()
	pending := runtime.pendingUpdaterActivation
	result.ScanRequested = runtime.updaterScanRequested
	runtime.updaterScanRequested = false
	runtime.updaterMu.Unlock()
	if pending == nil {
		return result, nil
	}
	var candidate model.BackupAssetUpdaterMetadata
	query := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where("id = ? AND state = ?", pending.CandidateID, "verified").Limit(1).Find(&candidate)
	if query.Error != nil {
		return processingupdater.PullActivationResult{}, query.Error
	}
	if query.RowsAffected != 1 {
		return processingupdater.PullActivationResult{}, backupasset.ErrConflict
	}
	expected := ""
	if pending.ExpectedActiveFingerprint != nil {
		expected = *pending.ExpectedActiveFingerprint
	}
	result.Directive = &processingupdater.ActivationDirective{
		SchemaVersion: 1, CandidateID: candidate.ID, ExpectedOldFingerprint: expected,
		NewFingerprint: candidate.BundleFingerprint,
	}
	return result, nil
}

func (runtime *managedProcessingRuntime) ReportUpdaterActivation(
	ctx context.Context,
	identity processingupdater.UpdaterTransportIdentity,
	request processingupdater.ActivationReportRequest,
) (processingupdater.ActivationReportResult, error) {
	if !validRuntimeUpdaterIdentity(identity) {
		return processingupdater.ActivationReportResult{}, processingupdater.ErrUpdaterUnauthenticated
	}
	if !runtime.updaterEnabled() {
		return processingupdater.ActivationReportResult{}, processing.ErrProcessingDisabled
	}
	if !validReportUpdaterRequest(request) {
		return processingupdater.ActivationReportResult{}, processingupdater.ErrProtocolInvalid
	}
	activationOutcome := processing.UpdaterActivationRollback
	if runtime.metrics != nil {
		defer func() { runtime.metrics.ObserveUpdaterActivation(activationOutcome) }()
	}
	runtime.updaterMu.Lock()
	defer runtime.updaterMu.Unlock()
	committed, err := runtime.committedUpdaterActivation(ctx, request.Receipt)
	if err != nil {
		return processingupdater.ActivationReportResult{}, err
	}
	if committed {
		if err := runtime.reconcileActivePipelines(ctx); err != nil {
			return processingupdater.ActivationReportResult{}, err
		}
		activationOutcome = processing.UpdaterActivationCommit
		return processingupdater.ActivationReportResult{
			SchemaVersion: 1, Decision: processingupdater.ActivationDecisionCommit,
			ActiveFingerprint: request.Receipt.NewFingerprint,
		}, nil
	}
	pending := runtime.pendingUpdaterActivation
	oldFingerprint := request.Receipt.OldFingerprint
	rollback := processingupdater.ActivationReportResult{
		SchemaVersion: 1, Decision: processingupdater.ActivationDecisionRollback, ActiveFingerprint: oldFingerprint,
	}
	if pending == nil || pending.CandidateID != request.Receipt.CandidateID ||
		pending.ExpectedActiveFingerprint == nil && oldFingerprint != "" ||
		pending.ExpectedActiveFingerprint != nil && *pending.ExpectedActiveFingerprint != oldFingerprint {
		return rollback, nil
	}
	if runtime.settings == nil {
		return rollback, nil
	}
	impact, ok := runtime.updaterCandidateImpacts[pending.CandidateID]
	if !ok {
		return rollback, nil
	}
	nextRegistry, err := runtime.replacementWorkerRegistry(request.Receipt.NewFingerprint)
	if err != nil {
		return rollback, nil
	}
	now := runtime.now().UTC()
	err = runtime.db.WithContext(nonNilRuntimeContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var candidate model.BackupAssetUpdaterMetadata
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND state = ? AND bundle_fingerprint = ?", pending.CandidateID, "verified", request.Receipt.NewFingerprint,
		).Limit(1).Find(&candidate)
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.Join(backupasset.ErrConflict, result.Error)
		}
		var activeRows []model.BackupAssetUpdaterMetadata
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state = ?", "active").Limit(2).Find(&activeRows)
		if result.Error != nil || len(activeRows) > 1 ||
			(len(activeRows) == 0 && oldFingerprint != "") ||
			(len(activeRows) == 1 && activeRows[0].BundleFingerprint != oldFingerprint) {
			return errors.Join(backupasset.ErrConflict, result.Error)
		}
		if len(activeRows) == 1 {
			updated := tx.Model(&model.BackupAssetUpdaterMetadata{}).Where("id = ? AND state = ?", activeRows[0].ID, "active").
				Updates(map[string]any{"state": "superseded", "updated_at": now})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return errors.Join(backupasset.ErrConflict, updated.Error)
			}
		}
		updatedAt := now
		if !updatedAt.After(candidate.UpdatedAt) {
			updatedAt = candidate.UpdatedAt.Add(time.Nanosecond)
		}
		updated := tx.Model(&model.BackupAssetUpdaterMetadata{}).Where("id = ? AND state = ?", candidate.ID, "verified").
			Updates(map[string]any{"state": "active", "activated_at": now, "updated_at": updatedAt})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errors.Join(backupasset.ErrConflict, updated.Error)
		}
		if !impact.affectContent && !impact.affectOCR {
			return nil
		}
		_, err := runtime.settings.AdvanceProcessingPipelineRevisionsTx(
			nonNilRuntimeContext(ctx), tx, impact.affectContent, impact.affectOCR,
		)
		return err
	})
	if err != nil {
		return rollback, nil
	}
	if nextRegistry != nil {
		runtime.mu.RLock()
		protocol := runtime.protocol
		runtime.mu.RUnlock()
		if err := protocol.ReplaceRegistry(nextRegistry); err != nil {
			return processingupdater.ActivationReportResult{}, err
		}
	}
	if err := runtime.reconcileActivePipelines(ctx); err != nil {
		return processingupdater.ActivationReportResult{}, err
	}
	runtime.pendingUpdaterActivation = nil
	activationOutcome = processing.UpdaterActivationCommit
	return processingupdater.ActivationReportResult{
		SchemaVersion: 1, Decision: processingupdater.ActivationDecisionCommit,
		ActiveFingerprint: request.Receipt.NewFingerprint,
	}, nil
}

func (runtime *managedProcessingRuntime) updaterEnabled() bool {
	config, err := runtime.ProcessingConfig()
	return err == nil && config.Enabled && config.Updater.Enabled
}

func (runtime *managedProcessingRuntime) activeUpdaterFingerprint(ctx context.Context) (string, error) {
	if runtime == nil {
		return "", processing.ErrProtocolUnavailable
	}
	return activeUpdaterFingerprintDB(nonNilRuntimeContext(ctx), runtime.db, false)
}

func activeUpdaterFingerprintDB(ctx context.Context, db *gorm.DB, lock bool) (string, error) {
	if db == nil {
		return "", processing.ErrProtocolUnavailable
	}
	var rows []model.BackupAssetUpdaterMetadata
	query := db.WithContext(nonNilRuntimeContext(ctx)).Where("state = ?", "active").Limit(2)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) > 1 {
		return "", backupasset.ErrConflict
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].BundleFingerprint, nil
}

func validRuntimeUpdaterIdentity(identity processingupdater.UpdaterTransportIdentity) bool {
	return len(identity.Fingerprint) == 64 && lowerHexRuntime(identity.Fingerprint) && identity.PeerPID >= 0
}

func processingUpdaterDeclarationForReceipt(
	receipt processingupdater.InboxReceipt,
) (processingUpdaterImpact, []ProcessingUpdaterCapabilityChange, error) {
	var impact processingUpdaterImpact
	changes := make([]ProcessingUpdaterCapabilityChange, 0, len(receipt.Capabilities))
	for _, declaration := range receipt.Capabilities {
		change := ProcessingUpdaterCapabilityChange{
			Capability: declaration.Capability, CapabilitySchema: declaration.Schema,
			Profiles: append([]string(nil), declaration.Profiles...),
		}
		for _, profileID := range declaration.Profiles {
			profile, ok := capabilityspec.Lookup(declaration.Capability, profileID, true)
			if !ok || profile.CapabilitySchema != declaration.Schema {
				return processingUpdaterImpact{}, nil, processingupdater.ErrProtocolInvalid
			}
			for _, output := range profile.Outputs {
				switch output.Role {
				case "content":
					impact.affectContent = true
				case "ocr":
					impact.affectOCR = true
				}
			}
		}
		changes = append(changes, change)
	}
	return impact, changes, nil
}

func validRegisterUpdaterRequest(request processingupdater.RegisterCandidateRequest) bool {
	payload, err := json.Marshal(request)
	if err != nil {
		return false
	}
	_, err = processingupdater.DecodeRegisterCandidateRequest(payload)
	return err == nil
}

func validPullUpdaterRequest(request processingupdater.PullActivationRequest) bool {
	payload, err := json.Marshal(request)
	if err != nil {
		return false
	}
	_, err = processingupdater.DecodePullActivationRequest(payload)
	return err == nil
}

func validReportUpdaterRequest(request processingupdater.ActivationReportRequest) bool {
	payload, err := json.Marshal(request)
	if err != nil {
		return false
	}
	_, err = processingupdater.DecodeActivationReportRequest(payload)
	return err == nil
}

func lowerHexRuntime(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return value != ""
}

func processingUpdaterCandidate(
	row model.BackupAssetUpdaterMetadata,
	changes []ProcessingUpdaterCapabilityChange,
) ProcessingUpdaterCandidate {
	return ProcessingUpdaterCandidate{
		CandidateID: row.ID, SourceKind: row.SourceKind, SourceID: row.SourceID, Version: row.Version,
		ManifestDigest: row.ManifestDigest, SigningKeyFingerprint: row.SigningKeyFingerprint,
		BundleFingerprint: row.BundleFingerprint, State: row.State, Reason: row.FailureCode,
		VerifiedAt: cloneProcessingTime(row.VerifiedAt), ActivatedAt: cloneProcessingTime(row.ActivatedAt),
		CapabilityChanges: cloneProcessingUpdaterChanges(changes),
	}
}

func cloneProcessingUpdaterChanges(changes []ProcessingUpdaterCapabilityChange) []ProcessingUpdaterCapabilityChange {
	result := make([]ProcessingUpdaterCapabilityChange, len(changes))
	for index, change := range changes {
		result[index] = change
		result[index].Profiles = append([]string(nil), change.Profiles...)
	}
	return result
}

func (runtime *managedProcessingRuntime) processingUpdaterChanges(candidateID string) []ProcessingUpdaterCapabilityChange {
	runtime.updaterMu.Lock()
	defer runtime.updaterMu.Unlock()
	return cloneProcessingUpdaterChanges(runtime.updaterCandidateChanges[candidateID])
}

func (runtime *managedProcessingRuntime) lastPersistentUpdaterAdmission(ctx context.Context) (time.Time, error) {
	var row model.BackupAssetUpdaterMetadata
	result := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where("updated_at > created_at").
		Order("updated_at DESC").Limit(1).Find(&row)
	if result.Error != nil {
		return time.Time{}, result.Error
	}
	if result.RowsAffected == 0 {
		return time.Time{}, nil
	}
	return row.UpdatedAt.UTC(), nil
}

func (runtime *managedProcessingRuntime) committedUpdaterActivation(
	ctx context.Context,
	receipt processingupdater.ActivationReceipt,
) (bool, error) {
	var candidate model.BackupAssetUpdaterMetadata
	result := runtime.db.WithContext(nonNilRuntimeContext(ctx)).Where(
		"id = ? AND state = ? AND bundle_fingerprint = ?", receipt.CandidateID, "active", receipt.NewFingerprint,
	).Limit(1).Find(&candidate)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	active, err := runtime.activeUpdaterFingerprint(ctx)
	if err != nil {
		return false, err
	}
	return active == receipt.NewFingerprint, nil
}

func newProcessingBackfillPolicy(config backupasset.ProcessingBackfillConfig) (ProcessingBackfillPolicy, error) {
	value := ProcessingBackfillPolicy{
		SchemaVersion: 1, Paused: config.Paused, BatchSize: config.BatchSize, JobsPerHour: config.JobsPerHour,
		BytesPerHour: config.BytesPerHour, ProviderConcurrency: config.ProviderConcurrency,
		CapabilityConcurrency: config.CapabilityConcurrency,
	}
	payload, err := json.Marshal(struct {
		Paused                bool  `json:"paused"`
		BatchSize             int   `json:"batch_size"`
		JobsPerHour           int   `json:"jobs_per_hour"`
		BytesPerHour          int64 `json:"bytes_per_hour"`
		ProviderConcurrency   int   `json:"provider_concurrency"`
		CapabilityConcurrency int   `json:"capability_concurrency"`
	}{value.Paused, value.BatchSize, value.JobsPerHour, value.BytesPerHour, value.ProviderConcurrency, value.CapabilityConcurrency})
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	digest := sha256.Sum256(payload)
	value.Revision = hex.EncodeToString(digest[:])
	return value, nil
}

func processingBackfillPolicyFromValues(values map[string]string) (ProcessingBackfillPolicy, error) {
	paused, err := strconv.ParseBool(values["backup_assets.processing_backfill_paused"])
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	batch, err := strconv.Atoi(values["backup_assets.processing_backfill_batch_size"])
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	jobs, err := strconv.Atoi(values["backup_assets.processing_backfill_jobs_per_hour"])
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	bytesPerHour, err := strconv.ParseInt(values["backup_assets.processing_backfill_bytes_per_hour"], 10, 64)
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	provider, err := strconv.Atoi(values["backup_assets.processing_backfill_provider_concurrency"])
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	capability, err := strconv.Atoi(values["backup_assets.processing_backfill_capability_concurrency"])
	if err != nil {
		return ProcessingBackfillPolicy{}, err
	}
	return newProcessingBackfillPolicy(backupasset.ProcessingBackfillConfig{
		Paused: paused, BatchSize: batch, JobsPerHour: jobs, BytesPerHour: bytesPerHour,
		ProviderConcurrency: provider, CapabilityConcurrency: capability,
	})
}

func nonNilRuntimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (runtime *managedProcessingRuntime) currentCapabilityService() *processing.CapabilityService {
	if runtime == nil || runtime.stopped.Load() {
		return nil
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.capabilityService
}

func (runtime *managedProcessingRuntime) processingEnabled() bool {
	if runtime == nil || runtime.foundation == nil || runtime.stopped.Load() {
		return false
	}
	enabled, enabledErr := runtime.foundation.FeatureEnabled()
	config, configErr := runtime.foundation.ProcessingConfig()
	return enabledErr == nil && configErr == nil && enabled && config.Enabled
}

func (runtime *managedProcessingRuntime) ProcessingConfig() (backupasset.ProcessingConfig, error) {
	if runtime == nil || runtime.foundation == nil {
		return backupasset.ProcessingConfig{}, fmt.Errorf("%w: Processing config unavailable", backupasset.ErrInvalidState)
	}
	return runtime.foundation.ProcessingConfig()
}

func (runtime *managedProcessingRuntime) AdminSummary(ctx context.Context) (ProcessingAdminSummary, error) {
	if runtime == nil || runtime.db == nil {
		return ProcessingAdminSummary{}, fmt.Errorf("%w: Processing summary unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.RLock()
	config := runtime.config
	lastReconciled := cloneProcessingTime(runtime.lastReconciled)
	runtime.mu.RUnlock()
	if config.QueueMax == 0 {
		var err error
		config, err = runtime.foundation.ProcessingConfig()
		if err != nil {
			return ProcessingAdminSummary{}, err
		}
	}
	backfillPolicy, err := newProcessingBackfillPolicy(config.Backfill)
	if err != nil {
		return ProcessingAdminSummary{}, err
	}
	summary := ProcessingAdminSummary{
		SchemaVersion: 1, BackfillPolicy: backfillPolicy,
		Configured:   config.LocalWorker.Enabled || config.RemoteWorker.Enabled,
		LocalEnabled: config.LocalWorker.Enabled, RemoteEnabled: config.RemoteWorker.Enabled,
		Slots:        ProcessingSlotSummary{InteractiveTotal: config.InteractiveSlots, BackgroundTotal: config.BackgroundSlots},
		Queue:        ProcessingQueueSummary{ByState: make(map[string]int64), ByPriority: make(map[string]int64)},
		Outcomes:     ProcessingOutcomeSummary{ByErrorCategory: make(map[string]int64)},
		Derived:      ProcessingDerivedSummary{ByState: make(map[string]int64), QuotaBytes: config.DerivedStore.GlobalMaxBytes},
		ReconciledAt: lastReconciled,
	}
	if !summary.Configured || !config.Enabled {
		return summary, nil
	}
	if err := runtime.loadWorkerSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	if err := runtime.loadJobSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	if err := runtime.loadDerivedSummary(ctx, &summary); err != nil {
		return ProcessingAdminSummary{}, err
	}
	return summary, nil
}

func (runtime *managedProcessingRuntime) loadWorkerSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var rows []struct {
		TrustState  string
		HealthState string
		Count       int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
		Select("trust_state, health_state, count(*) AS count").Group("trust_state, health_state").Scan(&rows).Error; err != nil {
		return fmt.Errorf("load Processing Worker aggregates: %w", err)
	}
	for _, row := range rows {
		switch {
		case row.TrustState == "quarantined":
			summary.Workers.Quarantined += row.Count
		case row.HealthState == "draining":
			summary.Workers.Draining += row.Count
		case row.HealthState == "ready":
			summary.Workers.Active += row.Count
		default:
			summary.Workers.Degraded += row.Count
		}
	}
	var slots []struct {
		SlotClass string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
		Select("slot_class, count(*) AS count").Where("state = ? AND is_current = ?", "active", true).
		Group("slot_class").Scan(&slots).Error; err != nil {
		return fmt.Errorf("load Processing slot aggregates: %w", err)
	}
	for _, row := range slots {
		switch processing.SlotClass(row.SlotClass) {
		case processing.SlotInteractive:
			summary.Slots.InteractiveUsed += row.Count
		case processing.SlotBackground, processing.SlotBackgroundBorrowed:
			summary.Slots.BackgroundUsed += row.Count
		}
	}
	return nil
}

func (runtime *managedProcessingRuntime) loadJobSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var states []struct {
		State         string
		PriorityClass string
		Count         int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("state, priority_class, count(*) AS count").Where("is_current = ?", true).
		Group("state, priority_class").Scan(&states).Error; err != nil {
		return fmt.Errorf("load Processing queue aggregates: %w", err)
	}
	for _, row := range states {
		summary.Queue.Total += row.Count
		summary.Queue.ByState[row.State] += row.Count
		summary.Queue.ByPriority[row.PriorityClass] += row.Count
	}
	var oldest model.BackupAssetProcessingJob
	result := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("queued_at").Where("state = ? AND is_current = ?", processing.ProcessingQueued, true).
		Order("queued_at ASC").Limit(1).Find(&oldest)
	if result.Error != nil {
		return fmt.Errorf("load oldest Processing queue age: %w", result.Error)
	}
	if result.RowsAffected == 1 && !oldest.QueuedAt.IsZero() {
		age := runtime.now().UTC().Sub(oldest.QueuedAt.UTC())
		if age > 0 {
			summary.Queue.OldestQueuedSeconds = int64(age / time.Second)
		}
	}
	var outcomes []struct {
		ErrorCode string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("error_code, count(*) AS count").Where("error_code <> ?", "").Group("error_code").Scan(&outcomes).Error; err != nil {
		return fmt.Errorf("load Processing outcome aggregates: %w", err)
	}
	for _, row := range outcomes {
		summary.Outcomes.ByErrorCategory[row.ErrorCode] += row.Count
	}
	return nil
}

func (runtime *managedProcessingRuntime) loadDerivedSummary(ctx context.Context, summary *ProcessingAdminSummary) error {
	var sets []struct {
		State        string
		LogicalBytes int64
		Count        int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedArtifactSet{}).
		Select("state, coalesce(sum(total_plaintext_bytes), 0) AS logical_bytes, count(*) AS count").
		Group("state").Scan(&sets).Error; err != nil {
		return fmt.Errorf("load Derived set aggregates: %w", err)
	}
	for _, row := range sets {
		summary.Derived.ByState[row.State] += row.Count
		if row.State == "active" || row.State == "stale" {
			summary.Derived.LogicalBytes += row.LogicalBytes
		}
	}
	var blobs []struct {
		State        string
		PhysicalSize int64
		RefCount     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Select("state, physical_size, ref_count").Scan(&blobs).Error; err != nil {
		return fmt.Errorf("load Derived blob aggregates: %w", err)
	}
	for _, row := range blobs {
		if row.State == "active" || row.State == "staged" {
			summary.Derived.PhysicalBytes += row.PhysicalSize
		}
		if row.RefCount == 0 {
			summary.Derived.OrphanBytes += row.PhysicalSize
		}
	}
	return nil
}

func (runtime *managedProcessingRuntime) reconcile(ctx context.Context) error {
	runtime.mu.RLock()
	reconciler := runtime.reconciler
	derivedReconciler := runtime.derivedReconciler
	sink := runtime.sink
	projectionBatchSize := runtime.config.DerivedStore.ReconcileBatchSize
	runtime.mu.RUnlock()
	if err := runtime.reconcileActivePipelines(ctx); err != nil {
		return err
	}
	if _, err := runtime.scheduleSecretContinuations(ctx); err != nil {
		return err
	}
	if reconciler == nil {
		return nil
	}
	if sink != nil {
		if _, err := sink.ReconcilePendingProjections(ctx, projectionBatchSize); err != nil {
			return err
		}
	}
	reconcileResult, err := reconciler.Reconcile(ctx)
	if err != nil {
		return err
	}
	for index := 0; index < reconcileResult.ExpiredAttempts; index++ {
		runtime.metrics.ObserveLeaseLoss()
	}
	if _, err := reconciler.PromoteRetries(ctx); err != nil {
		return err
	}
	if derivedReconciler != nil {
		derivedResult, err := derivedReconciler.Reconcile(ctx)
		if err != nil {
			runtime.metrics.ObserveDerived(processing.DerivedEventReconcileFailure)
			return err
		}
		for index := 0; index < derivedResult.RewrappedBlobs; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventRewrapped)
		}
		for index := 0; index < derivedResult.RepairedRefCounts; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventRefcountRepaired)
		}
		for index := 0; index < derivedResult.PurgedBlobs; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventPurged)
		}
		for index := 0; index < derivedResult.RemovedFileOrphans; index++ {
			runtime.metrics.ObserveDerived(processing.DerivedEventOrphanRemoved)
		}
	}
	runtime.publishMetrics(ctx)
	now := runtime.now().UTC()
	runtime.mu.Lock()
	runtime.lastReconciled = &now
	runtime.mu.Unlock()
	return nil
}

func (runtime *managedProcessingRuntime) publishMetrics(ctx context.Context) {
	if runtime == nil || runtime.metrics == nil {
		return
	}
	for _, trust := range []processing.WorkerTrustClass{processing.WorkerTrustActive, processing.WorkerTrustQuarantined, processing.WorkerTrustRevoked} {
		for _, health := range []processing.WorkerHealthClass{processing.WorkerHealthReady, processing.WorkerHealthDegraded, processing.WorkerHealthDraining} {
			runtime.metrics.SetWorkers(trust, health, 0)
		}
	}
	var workers []struct {
		TrustState  string
		HealthState string
		Count       int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
		Select("trust_state, health_state, count(*) AS count").Group("trust_state, health_state").Scan(&workers).Error; err == nil {
		for _, row := range workers {
			runtime.metrics.SetWorkers(processing.WorkerTrustClass(row.TrustState), processing.WorkerHealthClass(row.HealthState), row.Count)
		}
	}
	for _, priority := range []processing.PriorityClass{processing.PriorityInteractive, processing.PriorityBackground} {
		for _, state := range processing.AllProcessingStates() {
			runtime.metrics.SetQueue(priority, state, 0, 0)
		}
	}
	var jobs []struct {
		State         string
		PriorityClass string
		Count         int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("state, priority_class, count(*) AS count").
		Where("is_current = ?", true).Group("state, priority_class").Scan(&jobs).Error; err == nil {
		for _, row := range jobs {
			age := time.Duration(0)
			var oldest model.BackupAssetProcessingJob
			if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
				Select("queued_at").
				Where("state = ? AND priority_class = ? AND is_current = ?", row.State, row.PriorityClass, true).
				Order("queued_at ASC").Limit(1).Take(&oldest).Error; err == nil {
				age = runtime.now().UTC().Sub(oldest.QueuedAt.UTC())
			}
			runtime.metrics.SetQueue(processing.PriorityClass(row.PriorityClass), processing.ProcessingState(row.State), row.Count, age)
		}
	}
	for _, class := range []processing.SlotClass{processing.SlotInteractive, processing.SlotBackground, processing.SlotBackgroundBorrowed} {
		runtime.metrics.SetSlots(class, processing.SlotMetricUsed, 0)
	}
	var attempts []struct {
		SlotClass string
		Count     int64
	}
	if err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
		Select("slot_class, count(*) AS count").Where("state = ? AND is_current = ?", "active", true).
		Group("slot_class").Scan(&attempts).Error; err == nil {
		for _, row := range attempts {
			runtime.metrics.SetSlots(processing.SlotClass(row.SlotClass), processing.SlotMetricUsed, row.Count)
		}
	}
	config := runtime.config
	runtime.metrics.SetSlots(processing.SlotInteractive, processing.SlotMetricTotal, int64(config.InteractiveSlots))
	runtime.metrics.SetSlots(processing.SlotBackground, processing.SlotMetricTotal, int64(config.BackgroundSlots))
	var derived struct {
		LogicalBytes  int64
		PhysicalBytes int64
		OrphanBytes   int64
	}
	_ = runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedArtifactSet{}).
		Select("coalesce(sum(CASE WHEN state IN ('active','stale') THEN total_plaintext_bytes ELSE 0 END), 0) AS logical_bytes").Scan(&derived).Error
	_ = runtime.db.WithContext(ctx).Model(&model.BackupAssetDerivedBlob{}).
		Select("coalesce(sum(CASE WHEN state IN ('active','staged') THEN physical_size ELSE 0 END), 0) AS physical_bytes, coalesce(sum(CASE WHEN ref_count = 0 THEN physical_size ELSE 0 END), 0) AS orphan_bytes").Scan(&derived).Error
	runtime.metrics.SetDerived(processing.DerivedMetricLogicalBytes, derived.LogicalBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricPhysicalBytes, derived.PhysicalBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricOrphanBytes, derived.OrphanBytes)
	runtime.metrics.SetDerived(processing.DerivedMetricQuotaBytes, config.DerivedStore.GlobalMaxBytes)
	coverageStates := []processing.CoverageMetricState{
		processing.CoverageMetricComplete, processing.CoverageMetricPartial, processing.CoverageMetricQueued,
		processing.CoverageMetricFailed, processing.CoverageMetricUnsupported,
		processing.CoverageMetricNotDeployed, processing.CoverageMetricStale,
	}
	for _, profile := range capabilityspec.AllProfiles(config.SecretClassify) {
		for _, state := range coverageStates {
			runtime.metrics.SetCoverage(profile.Capability, profile.OutputProfile, state, 0)
		}
	}
	runtime.mu.RLock()
	coverageService := runtime.coverageService
	runtime.mu.RUnlock()
	if coverageService == nil {
		return
	}
	coverage, err := coverageService.Summary(ctx)
	if err != nil {
		return
	}
	for _, bucket := range coverage.ByCapability {
		values := map[processing.CoverageMetricState]int64{
			processing.CoverageMetricComplete:    bucket.Completed,
			processing.CoverageMetricPartial:     bucket.Partial,
			processing.CoverageMetricQueued:      bucket.Queued,
			processing.CoverageMetricFailed:      bucket.Failed,
			processing.CoverageMetricUnsupported: bucket.Unsupported,
			processing.CoverageMetricNotDeployed: bucket.NotDeployed,
			processing.CoverageMetricStale:       bucket.Stale,
		}
		for state, count := range values {
			runtime.metrics.SetCoverage(bucket.Capability, bucket.Profile, state, count)
		}
	}
}

func (runtime *managedProcessingRuntime) Run(ctx context.Context) {
	if runtime == nil || !runtime.ready.Load() || runtime.stopped.Load() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.Lock()
	if runtime.runDone != nil {
		runtime.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	runtime.runCancel = cancel
	runtime.runDone = done
	interval := runtime.config.DerivedStore.ReconcileInterval
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		if runtime.runDone == done {
			runtime.runCancel = nil
			runtime.runDone = nil
			close(done)
		}
		runtime.mu.Unlock()
	}()
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			_ = runtime.reconcile(runCtx)
		}
	}
}

func (runtime *managedProcessingRuntime) StopAccepting() {
	if runtime == nil {
		return
	}
	runtime.ready.Store(false)
	runtime.mu.RLock()
	protocol := runtime.workerProtocol
	runtime.mu.RUnlock()
	if protocol != nil {
		protocol.StopAccepting()
	}
}

func (runtime *managedProcessingRuntime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.stopped.Store(true)
	runtime.StopAccepting()
	runtime.mu.Lock()
	cancel := runtime.runCancel
	done := runtime.runDone
	protocol := runtime.workerProtocol
	reconciler := runtime.reconciler
	derivedReconciler := runtime.derivedReconciler
	runtime.mu.Unlock()
	var shutdownErrors []error
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, ctx.Err())
		}
	}
	if protocol != nil {
		if err := protocol.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if reconciler != nil {
		if _, err := reconciler.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if derivedReconciler != nil {
		if _, err := derivedReconciler.Reconcile(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}

func cloneProcessingTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

type runtimeProcessingSourceRevalidator struct {
	source content.SourceResolver
}

func (revalidator runtimeProcessingSourceRevalidator) RevalidateProcessingSource(ctx context.Context, descriptor processing.WorkDescriptorV1) error {
	if revalidator.source == nil || processing.ValidateWorkDescriptorV1(descriptor) != nil {
		return processing.ErrManifestSourceChanged
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := revalidator.source.OpenContentSource(ctx, content.SourceRequest{
		Ref: descriptor.Source, CatalogGenerationID: descriptor.CatalogGenerationID,
		ExpectedSource: descriptor.SourceFingerprint, ExpectedEntry: descriptor.EntryFingerprint,
		Mode: content.SourceModeStat,
	})
	if err != nil || session == nil {
		return fmt.Errorf("%w: source unavailable", processing.ErrManifestSourceChanged)
	}
	stat := session.Stat()
	revalidateErr := session.Revalidate(ctx)
	closeErr := session.Close()
	if stat.SourceFingerprint != descriptor.SourceFingerprint || stat.EntryFingerprint != descriptor.EntryFingerprint ||
		revalidateErr != nil || closeErr != nil {
		return processing.ErrManifestSourceChanged
	}
	return nil
}

type runtimeProjectionRevisions struct {
	Content int64
	OCR     int64
}

type runtimePipelineRevisionSource func(context.Context) (runtimeProjectionRevisions, error)

type runtimeDerivedProjectionPort struct {
	db                *gorm.DB
	ingest            search.ContentIndexIngestTx
	classification    search.ClassificationIndexIngestTx
	pipelineRevisions runtimePipelineRevisionSource
}

type runtimeProjectionControl struct {
	generation model.BackupAssetSearchGeneration
	document   model.BackupAssetSearchDocument
	fields     map[search.SearchField]model.BackupAssetSearchDocumentField
}

type runtimePreparedDerivedProjection struct {
	port             runtimeDerivedProjectionPort
	artifactSetID    string
	searchGeneration string
	projections      []search.PreparedContentProjection
	classification   *search.PreparedClassificationProjection
}

type runtimePreparedDerivedRevocation struct {
	port           runtimeDerivedProjectionPort
	revocations    []search.RevokeProjection
	classification *search.PreparedClassificationProjection
}

func (port runtimeDerivedProjectionPort) PreparePublish(
	ctx context.Context,
	request processing.DerivedProjectionPublish,
) (processing.PreparedDerivedProjection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !port.valid() || backupasset.ValidateOpaqueID(request.ArtifactSetID) != nil ||
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID}) != nil ||
		backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil || request.SourceFingerprint == "" ||
		(request.Classification == nil && (len(request.Fields) == 0 || len(request.Fields) > 2)) ||
		request.Classification != nil && len(request.Fields) != 0 {
		return nil, fmt.Errorf("%w: invalid Derived Search publication", backupasset.ErrInvalidState)
	}
	if request.Classification != nil {
		return port.prepareClassificationProjection(ctx, request)
	}
	revisions, err := port.pipelineRevisions(ctx)
	if err != nil || revisions.Content <= 0 || revisions.OCR <= 0 {
		return nil, fmt.Errorf("%w: processing pipeline revisions unavailable", backupasset.ErrInvalidState)
	}
	fields := make([]search.SearchField, 0, len(request.Fields))
	seen := make(map[search.SearchField]bool, len(request.Fields))
	for _, field := range request.Fields {
		mapped, mapErr := runtimeSearchField(field.Role)
		if mapErr != nil || seen[mapped] || backupasset.ValidateOpaqueID(field.ExcerptArtifactID) != nil ||
			(field.Completeness != processing.ArtifactComplete && field.Completeness != processing.ArtifactPartial) {
			return nil, fmt.Errorf("%w: invalid Derived Search field", backupasset.ErrInvalidState)
		}
		seen[mapped] = true
		fields = append(fields, mapped)
	}
	control, err := port.loadControl(ctx, request.RecoveryPointID, request.CatalogGenerationID, request.EntryID, request.SourceFingerprint, fields)
	if err != nil {
		return nil, err
	}
	prepared := make([]search.PreparedContentProjection, 0, len(request.Fields))
	for _, field := range request.Fields {
		mapped, _ := runtimeSearchField(field.Role)
		row := control.fields[mapped]
		pipelineRevision, revisionErr := nextRuntimeProjectionRevision(row.PipelineRevision, runtimePipelineRevision(revisions, mapped))
		if revisionErr != nil {
			return nil, revisionErr
		}
		terms := make([]search.TermFrequency, len(field.Terms))
		for index, term := range field.Terms {
			terms[index] = search.TermFrequency{Term: term.Term, Frequency: term.Frequency}
		}
		excerpt := field.ExcerptArtifactID
		projection := search.ContentProjection{
			Ref:   backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
			Field: mapped, Terms: terms, SourceFingerprint: request.SourceFingerprint,
			CatalogGenerationID: request.CatalogGenerationID, SearchGenerationID: control.generation.ID,
			ProcessingLeaseID: request.RecoveryPointFence.LeaseID, AttemptID: request.RecoveryPointFence.AttemptID,
			FenceToken:                     request.RecoveryPointFence.FenceToken,
			ExpectedClassificationRevision: control.document.ClassificationRevision,
			Classification:                 search.Sensitivity(control.document.Sensitivity),
			ClassificationRevision:         control.document.ClassificationRevision,
			CoverageRevision:               row.CoverageRevision + 1,
			PipelineRevision:               pipelineRevision,
			IndexRevision:                  row.IndexRevision + 1,
			ExcerptRef:                     &excerpt,
			Coverage:                       runtimeFieldCoverage(field.Completeness),
		}
		value, prepareErr := port.ingest.PrepareContentProjection(ctx, projection)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare Search content projection: %w", prepareErr)
		}
		prepared = append(prepared, value)
	}
	return &runtimePreparedDerivedProjection{
		port: port, artifactSetID: request.ArtifactSetID,
		searchGeneration: control.generation.ID, projections: prepared,
	}, nil
}

func (port runtimeDerivedProjectionPort) prepareClassificationProjection(
	ctx context.Context,
	request processing.DerivedProjectionPublish,
) (processing.PreparedDerivedProjection, error) {
	evidence := request.Classification
	if port.classification == nil || evidence == nil || backupasset.ValidateOpaqueID(evidence.ArtifactID) != nil ||
		!validRuntimeClassificationEvidence(*evidence) {
		return nil, fmt.Errorf("%w: invalid Derived classification evidence", backupasset.ErrInvalidState)
	}
	fields := []search.SearchField{search.SearchFieldContent, search.SearchFieldOCR}
	control, err := port.loadControl(
		ctx, request.RecoveryPointID, request.CatalogGenerationID, request.EntryID, request.SourceFingerprint, fields,
	)
	if err != nil {
		return nil, err
	}
	classification, err := runtimeSearchSensitivity(evidence.Sensitivity)
	if err != nil {
		return nil, err
	}
	projection := search.ClassificationProjection{
		Ref:               backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
		SourceFingerprint: request.SourceFingerprint, CatalogGenerationID: request.CatalogGenerationID,
		SearchGenerationID: control.generation.ID, ProcessingLeaseID: request.RecoveryPointFence.LeaseID,
		AttemptID: request.RecoveryPointFence.AttemptID, FenceToken: request.RecoveryPointFence.FenceToken,
		ExpectedClassificationRevision: control.document.ClassificationRevision,
		Classification:                 classification,
		ClassificationRevision:         control.document.ClassificationRevision + 1,
		EvidenceArtifactID:             evidence.ArtifactID,
	}
	prepared, err := port.classification.PrepareClassificationProjection(ctx, projection)
	if err != nil {
		return nil, fmt.Errorf("prepare Search classification projection: %w", err)
	}
	return &runtimePreparedDerivedProjection{
		port: port, artifactSetID: request.ArtifactSetID, searchGeneration: control.generation.ID,
		classification: &prepared,
	}, nil
}

func (prepared *runtimePreparedDerivedProjection) PublishTx(
	ctx context.Context,
	tx *gorm.DB,
) (processing.DerivedProjectionPublication, error) {
	if prepared == nil || tx == nil || !prepared.port.valid() || backupasset.ValidateOpaqueID(prepared.artifactSetID) != nil ||
		backupasset.ValidateOpaqueID(prepared.searchGeneration) != nil ||
		(len(prepared.projections) == 0) == (prepared.classification == nil) {
		return processing.DerivedProjectionPublication{}, fmt.Errorf("%w: invalid prepared Search projection", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, projection := range prepared.projections {
		if err := prepared.port.ingest.PublishContentProjectionTx(ctx, tx, projection); err != nil {
			return processing.DerivedProjectionPublication{}, err
		}
	}
	if prepared.classification != nil {
		if err := prepared.port.classification.PublishClassificationProjectionTx(ctx, tx, *prepared.classification); err != nil {
			return processing.DerivedProjectionPublication{}, err
		}
	}
	var revision int64
	result := tx.WithContext(ctx).Model(&model.BackupAssetSearchGeneration{}).
		Select("projection_revision").Where("id = ?", prepared.searchGeneration).Scan(&revision)
	if result.Error != nil || result.RowsAffected != 1 || revision <= 0 {
		return processing.DerivedProjectionPublication{}, fmt.Errorf("%w: Search projection receipt unavailable", backupasset.ErrInvalidState)
	}
	return processing.DerivedProjectionPublication{ArtifactSetID: prepared.artifactSetID, Revision: revision}, nil
}

func (port runtimeDerivedProjectionPort) PrepareRevoke(
	ctx context.Context,
	request processing.DerivedProjectionRevoke,
) (processing.PreparedDerivedRevocation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !port.valid() || backupasset.ValidateOpaqueID(request.ArtifactSetID) != nil || request.ProjectionRevision <= 0 ||
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID}) != nil ||
		backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil || request.SourceFingerprint == "" {
		return nil, fmt.Errorf("%w: invalid Derived Search revocation", backupasset.ErrInvalidState)
	}
	var artifacts []model.BackupAssetDerivedArtifact
	if err := port.db.WithContext(ctx).Select("id", "role", "media_type").Where(
		"artifact_set_id = ? AND ((media_type = ? AND role IN ?) OR (media_type = ? AND role = ?))",
		request.ArtifactSetID, "text/plain", []string{string(processing.ArtifactRoleContent), string(processing.ArtifactRoleOCR)},
		"application/json", string(processing.ArtifactRoleMetadata),
	).Order("ordinal ASC").Find(&artifacts).Error; err != nil || len(artifacts) == 0 {
		return nil, fmt.Errorf("%w: Derived Search fields unavailable", backupasset.ErrInvalidState)
	}
	if len(artifacts) == 1 && artifacts[0].Role == string(processing.ArtifactRoleMetadata) && artifacts[0].MediaType == "application/json" {
		return port.prepareClassificationRevocation(ctx, request, artifacts[0])
	}
	fields := make([]search.SearchField, 0, 2)
	seen := make(map[search.SearchField]bool, 2)
	for _, artifact := range artifacts {
		if artifact.MediaType != "text/plain" || artifact.Role != string(processing.ArtifactRoleContent) && artifact.Role != string(processing.ArtifactRoleOCR) {
			continue
		}
		field, fieldErr := runtimeSearchField(processing.ArtifactRole(artifact.Role))
		if fieldErr != nil || seen[field] {
			return nil, fmt.Errorf("%w: invalid Derived Search revocation field", backupasset.ErrInvalidState)
		}
		seen[field] = true
		fields = append(fields, field)
	}
	if len(fields) == 0 || len(fields) > 2 {
		return nil, fmt.Errorf("%w: Derived Search fields unavailable", backupasset.ErrInvalidState)
	}
	revisions, err := port.pipelineRevisions(ctx)
	if err != nil || revisions.Content <= 0 || revisions.OCR <= 0 {
		return nil, fmt.Errorf("%w: processing pipeline revisions unavailable", backupasset.ErrInvalidState)
	}
	control, err := port.loadControl(ctx, request.RecoveryPointID, request.CatalogGenerationID, request.EntryID, request.SourceFingerprint, fields)
	if err != nil {
		return nil, err
	}
	revocations := make([]search.RevokeProjection, 0, len(fields))
	for _, field := range fields {
		row := control.fields[field]
		pipelineRevision, revisionErr := nextRuntimeProjectionRevision(row.PipelineRevision, runtimePipelineRevision(revisions, field))
		if revisionErr != nil {
			return nil, revisionErr
		}
		revocations = append(revocations, search.RevokeProjection{
			Ref: backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID}, Field: field,
			SourceFingerprint: request.SourceFingerprint, CatalogGenerationID: request.CatalogGenerationID,
			SearchGenerationID: control.generation.ID, ProcessingLeaseID: request.RecoveryPointFence.LeaseID,
			AttemptID: request.RecoveryPointFence.AttemptID, FenceToken: request.RecoveryPointFence.FenceToken,
			ExpectedClassificationRevision: control.document.ClassificationRevision,
			CoverageRevision:               row.CoverageRevision + 1, PipelineRevision: pipelineRevision, IndexRevision: row.IndexRevision + 1,
		})
	}
	return &runtimePreparedDerivedRevocation{port: port, revocations: revocations}, nil
}

func (port runtimeDerivedProjectionPort) prepareClassificationRevocation(
	ctx context.Context,
	request processing.DerivedProjectionRevoke,
	artifact model.BackupAssetDerivedArtifact,
) (processing.PreparedDerivedRevocation, error) {
	if port.classification == nil || backupasset.ValidateOpaqueID(artifact.ID) != nil {
		return nil, fmt.Errorf("%w: invalid Derived classification revocation", backupasset.ErrInvalidState)
	}
	var set model.BackupAssetDerivedArtifactSet
	result := port.db.WithContext(ctx).Select(
		"id", "job_id", "recovery_point_id", "catalog_generation_id", "entry_id", "source_fingerprint",
	).Where("id = ?", request.ArtifactSetID).Limit(1).Find(&set)
	if result.Error != nil || result.RowsAffected != 1 || set.RecoveryPointID != request.RecoveryPointID ||
		set.CatalogGenerationID != request.CatalogGenerationID || set.EntryID != request.EntryID ||
		set.SourceFingerprint != request.SourceFingerprint {
		return nil, fmt.Errorf("%w: Derived classification set unavailable", backupasset.ErrInvalidState)
	}
	var job model.BackupAssetProcessingJob
	result = port.db.WithContext(ctx).Select("id", "capability").Where("id = ?", set.JobID).Limit(1).Find(&job)
	if result.Error != nil || result.RowsAffected != 1 || job.Capability != "secret.classify" {
		return nil, fmt.Errorf("%w: Derived classification job unavailable", backupasset.ErrInvalidState)
	}
	control, err := port.loadControl(
		ctx, request.RecoveryPointID, request.CatalogGenerationID, request.EntryID, request.SourceFingerprint,
		[]search.SearchField{search.SearchFieldContent, search.SearchFieldOCR},
	)
	if err != nil {
		return nil, err
	}
	projection := search.ClassificationProjection{
		Ref:               backupasset.AssetRef{RecoveryPointID: request.RecoveryPointID, EntryID: request.EntryID},
		SourceFingerprint: request.SourceFingerprint, CatalogGenerationID: request.CatalogGenerationID,
		SearchGenerationID: control.generation.ID, ProcessingLeaseID: request.RecoveryPointFence.LeaseID,
		AttemptID: request.RecoveryPointFence.AttemptID, FenceToken: request.RecoveryPointFence.FenceToken,
		ExpectedClassificationRevision: control.document.ClassificationRevision,
		Classification:                 search.SensitivityUnknown,
		ClassificationRevision:         control.document.ClassificationRevision + 1,
	}
	prepared, err := port.classification.PrepareClassificationProjection(ctx, projection)
	if err != nil {
		return nil, fmt.Errorf("prepare Search classification revocation: %w", err)
	}
	return &runtimePreparedDerivedRevocation{port: port, classification: &prepared}, nil
}

func (prepared *runtimePreparedDerivedRevocation) RevokeTx(ctx context.Context, tx *gorm.DB) error {
	if prepared == nil || tx == nil || !prepared.port.valid() ||
		(len(prepared.revocations) == 0) == (prepared.classification == nil) {
		return fmt.Errorf("%w: invalid prepared Search revocation", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, revocation := range prepared.revocations {
		if err := prepared.port.ingest.RevokeContentProjectionTx(ctx, tx, revocation); err != nil {
			return err
		}
	}
	if prepared.classification != nil {
		if err := prepared.port.classification.PublishClassificationProjectionTx(ctx, tx, *prepared.classification); err != nil {
			return err
		}
	}
	return nil
}

func (port runtimeDerivedProjectionPort) valid() bool {
	return port.db != nil && port.ingest != nil && port.pipelineRevisions != nil
}

func (port runtimeDerivedProjectionPort) loadControl(
	ctx context.Context,
	recoveryPointID string,
	catalogGenerationID string,
	entryID string,
	sourceFingerprint string,
	fields []search.SearchField,
) (runtimeProjectionControl, error) {
	var control runtimeProjectionControl
	result := port.db.WithContext(ctx).Where(
		"recovery_point_id = ? AND catalog_generation_id = ? AND source_fingerprint = ? AND is_active = ? AND state = ?",
		recoveryPointID, catalogGenerationID, sourceFingerprint, true, search.SearchGenerationComplete,
	).Order("generation DESC").Limit(1).Find(&control.generation)
	if result.Error != nil || result.RowsAffected != 1 {
		return runtimeProjectionControl{}, fmt.Errorf("%w: active Search generation unavailable", backupasset.ErrInvalidState)
	}
	result = port.db.WithContext(ctx).Where(
		"search_generation_id = ? AND document_id = ? AND recovery_point_id = ? AND catalog_generation_id = ? AND entry_id = ?",
		control.generation.ID, entryID, recoveryPointID, catalogGenerationID, entryID,
	).Limit(1).Find(&control.document)
	if result.Error != nil || result.RowsAffected != 1 || control.document.ClassificationRevision <= 0 {
		return runtimeProjectionControl{}, fmt.Errorf("%w: Search document unavailable", backupasset.ErrInvalidState)
	}
	control.fields = make(map[search.SearchField]model.BackupAssetSearchDocumentField, len(fields))
	for _, field := range fields {
		var row model.BackupAssetSearchDocumentField
		result = port.db.WithContext(ctx).Where(
			"search_generation_id = ? AND document_id = ? AND field = ?", control.generation.ID, entryID, field,
		).Limit(1).Find(&row)
		if result.Error != nil || result.RowsAffected != 1 || row.ClassificationRevision != control.document.ClassificationRevision ||
			row.SourceFingerprint != sourceFingerprint || row.CoverageRevision <= 0 || row.PipelineRevision <= 0 || row.IndexRevision <= 0 {
			return runtimeProjectionControl{}, fmt.Errorf("%w: Search field unavailable", backupasset.ErrInvalidState)
		}
		control.fields[field] = row
	}
	return control, nil
}

func runtimeSearchField(role processing.ArtifactRole) (search.SearchField, error) {
	switch role {
	case processing.ArtifactRoleContent:
		return search.SearchFieldContent, nil
	case processing.ArtifactRoleOCR:
		return search.SearchFieldOCR, nil
	default:
		return "", backupasset.ErrInvalidState
	}
}

func runtimePipelineRevision(revisions runtimeProjectionRevisions, field search.SearchField) int64 {
	if field == search.SearchFieldOCR {
		return revisions.OCR
	}
	return revisions.Content
}

func validRuntimeClassificationEvidence(evidence processing.DerivedClassificationEvidence) bool {
	switch evidence.Sensitivity {
	case processing.DerivedSensitivityPublic, processing.DerivedSensitivityUnknown:
		return len(evidence.Categories) == 0
	case processing.DerivedSensitivitySecret:
		return len(evidence.Categories) == 1 && evidence.Categories[0] == "credential_pattern"
	default:
		return false
	}
}

func runtimeSearchSensitivity(value processing.DerivedSensitivity) (search.Sensitivity, error) {
	switch value {
	case processing.DerivedSensitivityPublic:
		return search.SensitivityNonSecret, nil
	case processing.DerivedSensitivitySecret:
		return search.SensitivitySecret, nil
	case processing.DerivedSensitivityUnknown:
		return search.SensitivityUnknown, nil
	default:
		return "", fmt.Errorf("%w: invalid Derived classification sensitivity", backupasset.ErrInvalidState)
	}
}

func nextRuntimeProjectionRevision(current int, active int64) (int, error) {
	maximum := int64(^uint(0) >> 1)
	if current <= 0 || active <= 0 || active > maximum || active < int64(current) {
		return 0, fmt.Errorf("%w: invalid Search pipeline revision", backupasset.ErrInvalidState)
	}
	return int(active), nil
}

func runtimeFieldCoverage(completeness processing.ArtifactCompleteness) search.FieldCoverage {
	if completeness == processing.ArtifactPartial {
		return search.FieldCoveragePartial
	}
	return search.FieldCoverageComplete
}
