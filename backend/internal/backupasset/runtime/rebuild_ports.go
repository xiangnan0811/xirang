package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type catalogBuilder interface {
	Build(context.Context, catalog.BuildRequest) (model.CatalogGeneration, error)
}

type catalogRebuildAdapter struct {
	builder catalogBuilder
}

func newCatalogRebuildAdapter(builder catalogBuilder) repository.CatalogRebuildStarter {
	return catalogRebuildAdapter{builder: builder}
}

func (adapter catalogRebuildAdapter) StartFreshCatalogGeneration(
	ctx context.Context,
	request repository.CatalogRebuildRequest,
) (repository.CatalogRebuildStart, error) {
	if adapter.builder == nil {
		return repository.CatalogRebuildStart{}, fmt.Errorf("%w: catalog rebuild builder unavailable", backupasset.ErrInvalidState)
	}
	generation, err := adapter.builder.Build(ctx, catalog.BuildRequest{
		RepositoryID:    request.RepositoryID,
		RecoveryPointID: request.RecoveryPointID,
	})
	if err != nil {
		return repository.CatalogRebuildStart{}, err
	}
	if backupasset.ValidateOpaqueID(generation.ID) != nil {
		return repository.CatalogRebuildStart{}, fmt.Errorf("%w: catalog rebuild generation", backupasset.ErrInvalidState)
	}
	return repository.CatalogRebuildStart{GenerationID: generation.ID}, nil
}

type derivedWorkRequester interface {
	RequestWork(context.Context, processing.WorkRequest) (processing.WorkResult, error)
}

const (
	defaultDerivedBackfillInspectedLimit = 200
	maxDerivedBackfillInspectedLimit     = 1000
	leftoverWalkUnprovenEntryID          = "leftover-walk-unproven"
	leftoverWalkUnprovenCapability       = "unproven"
)

type derivedBackfillCollectResult struct {
	descriptors []processing.WorkDescriptorV1
	incomplete  bool
}

type derivedBackfillWalkState struct {
	afterEntryID             string
	complete                 bool
	advertisementFingerprint string
	failedCanceledRevision   string
}

type derivedBackfillAdapter struct {
	requestWork derivedWorkRequester
	descriptors func(context.Context, repository.DerivedBackfillRequest) ([]processing.WorkDescriptorV1, error)
	expected    func(context.Context, repository.DerivedBackfillRequest) (derivedBackfillCollectResult, error)
}

func newDerivedBackfillAdapter(runtime *managedProcessingRuntime) repository.DerivedBackfillQueuer {
	if runtime == nil {
		return derivedBackfillAdapter{}
	}
	return derivedBackfillAdapter{
		requestWork: runtime,
		descriptors: runtime.rebuildDerivedDescriptors,
		expected:    runtime.collectUnprovenRebuildDerivedDescriptors,
	}
}

func (adapter derivedBackfillAdapter) QueueLowPriorityDerivedBackfill(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
) (int, error) {
	if backupasset.ValidateOpaqueID(request.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil {
		return 0, fmt.Errorf("%w: invalid derived backfill request", backupasset.ErrInvalidState)
	}
	if adapter.requestWork == nil || adapter.descriptors == nil {
		return 0, fmt.Errorf("%w: derived backfill requester unavailable", backupasset.ErrInvalidState)
	}
	descriptors, err := adapter.descriptors(ctx, request)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, descriptor := range descriptors {
		if err := processing.ValidateWorkDescriptorV1(descriptor); err != nil {
			return queued, err
		}
		_, err := adapter.requestWork.RequestWork(ctx, processing.WorkRequest{
			Descriptor: descriptor,
			Interest: processing.InterestRequest{
				OwnerKind:     processing.InterestSystem,
				OwnerKey:      rebuildDerivedOwnerKey(request.CatalogGenerationID, descriptor),
				PriorityClass: processing.PriorityBackground,
				Priority:      100,
			},
		})
		if errors.Is(err, processing.ErrNotDeployed) {
			return queued, fmt.Errorf("%w: derived backfill not deployed", err)
		}
		if err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

func (adapter derivedBackfillAdapter) ExpectedDescriptors(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
) ([]repository.ExpectedDerivedDescriptor, error) {
	var result derivedBackfillCollectResult
	var err error
	switch {
	case adapter.expected != nil:
		result, err = adapter.expected(ctx, request)
	case adapter.descriptors != nil:
		var descriptors []processing.WorkDescriptorV1
		descriptors, err = adapter.descriptors(ctx, request)
		result.descriptors = descriptors
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if result.incomplete && len(result.descriptors) == 0 {
		return []repository.ExpectedDerivedDescriptor{{
			EntryID: leftoverWalkUnprovenEntryID, Capability: leftoverWalkUnprovenCapability,
		}}, nil
	}
	expected := make([]repository.ExpectedDerivedDescriptor, 0, len(result.descriptors))
	for _, descriptor := range result.descriptors {
		expected = append(expected, repository.ExpectedDerivedDescriptor{
			EntryID: descriptor.Source.EntryID, Capability: descriptor.Capability,
		})
	}
	return expected, nil
}

func rebuildDerivedOwnerKey(generationID string, descriptor processing.WorkDescriptorV1) string {
	digest := sha256.Sum256([]byte("xirang.processing.rebuild-backfill.v1\x00" +
		generationID + "\x00" + descriptor.Source.EntryID + "\x00" + descriptor.Capability))
	return "rebuild:" + hex.EncodeToString(digest[:])
}

func (runtime *managedProcessingRuntime) RequestWork(
	ctx context.Context,
	request processing.WorkRequest,
) (processing.WorkResult, error) {
	if runtime == nil {
		return processing.WorkResult{}, processing.ErrNotDeployed
	}
	runtime.mu.RLock()
	coordinator := runtime.coordinator
	runtime.mu.RUnlock()
	if coordinator == nil {
		return processing.WorkResult{}, processing.ErrNotDeployed
	}
	return coordinator.RequestWork(ctx, request)
}

func (runtime *managedProcessingRuntime) QueueLowPriorityDerivedBackfill(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
) (int, error) {
	return newDerivedBackfillAdapter(runtime).QueueLowPriorityDerivedBackfill(ctx, request)
}

func (runtime *managedProcessingRuntime) collectUnprovenRebuildDerivedDescriptors(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
) (derivedBackfillCollectResult, error) {
	return runtime.collectRebuildDerivedDescriptors(ctx, request, false)
}

func (runtime *managedProcessingRuntime) rebuildDerivedDescriptors(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
) ([]processing.WorkDescriptorV1, error) {
	result, err := runtime.collectRebuildDerivedDescriptors(ctx, request, true)
	return result.descriptors, err
}

func derivedBackfillWalkKey(generationID, recoveryPointID string) string {
	return generationID + "\x00" + recoveryPointID
}

func derivedBackfillInspectedLimit(configured int) int {
	if configured < 1 {
		return defaultDerivedBackfillInspectedLimit
	}
	if configured > maxDerivedBackfillInspectedLimit {
		return maxDerivedBackfillInspectedLimit
	}
	return configured
}

func derivedBackfillAdvertisementFingerprint(capabilities []string, secretEnabled bool) string {
	secretLabel := "secret=0"
	if secretEnabled {
		secretLabel = "secret=1"
	}
	if len(capabilities) == 0 {
		return secretLabel
	}
	cloned := append([]string(nil), capabilities...)
	sort.Strings(cloned)
	return strings.Join(cloned, "\x00") + "\x00" + secretLabel
}

func (runtime *managedProcessingRuntime) derivedBackfillFailedCanceledRevision(
	ctx context.Context,
	generationID, recoveryPointID string,
) (string, error) {
	if runtime == nil || runtime.db == nil {
		return "0", nil
	}
	failedCanceled := []string{string(processing.ProcessingFailed), string(processing.ProcessingCanceled)}
	var count int64
	err := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("catalog_generation_id = ? AND recovery_point_id = ? AND state IN ?",
			generationID, recoveryPointID, failedCanceled).
		Count(&count).Error
	if err != nil {
		return "", fmt.Errorf("load rebuild derived failed-canceled count: %w", err)
	}
	if count < 1 {
		return "0", nil
	}
	var latest model.BackupAssetProcessingJob
	if err := runtime.db.WithContext(ctx).Select("id", "updated_at").
		Where("catalog_generation_id = ? AND recovery_point_id = ? AND state IN ?",
			generationID, recoveryPointID, failedCanceled).
		Order("updated_at DESC, id DESC").Limit(1).Take(&latest).Error; err != nil {
		return "", fmt.Errorf("load rebuild derived failed-canceled revision: %w", err)
	}
	return fmt.Sprintf("%d\x00%s\x00%s", count, latest.UpdatedAt.UTC().Format(time.RFC3339Nano), latest.ID), nil
}

func (runtime *managedProcessingRuntime) loadDerivedBackfillWalk(generationID, recoveryPointID string) derivedBackfillWalkState {
	if runtime == nil {
		return derivedBackfillWalkState{}
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if runtime.derivedBackfillWalk == nil {
		return derivedBackfillWalkState{}
	}
	return runtime.derivedBackfillWalk[derivedBackfillWalkKey(generationID, recoveryPointID)]
}

func (runtime *managedProcessingRuntime) persistDerivedBackfillWalk(generationID, recoveryPointID string, state derivedBackfillWalkState) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.derivedBackfillWalk == nil {
		runtime.derivedBackfillWalk = map[string]derivedBackfillWalkState{}
	}
	runtime.derivedBackfillWalk[derivedBackfillWalkKey(generationID, recoveryPointID)] = state
}

func (runtime *managedProcessingRuntime) collectRebuildDerivedDescriptors(
	ctx context.Context,
	request repository.DerivedBackfillRequest,
	applyAdmission bool,
) (derivedBackfillCollectResult, error) {
	if runtime == nil || runtime.db == nil {
		return derivedBackfillCollectResult{}, nil
	}
	ctx = nonNilRuntimeContext(ctx)
	runtime.mu.RLock()
	coordinator := runtime.coordinator
	secretEnabled := runtime.config.SecretClassify
	inspectedLimit := derivedBackfillInspectedLimit(runtime.config.Backfill.InspectedLimit)
	policy := processing.BackfillPolicy{
		Paused: runtime.config.Backfill.Paused, BatchSize: runtime.config.Backfill.BatchSize,
		JobsPerHour: runtime.config.Backfill.JobsPerHour, BytesPerHour: runtime.config.Backfill.BytesPerHour,
		ProviderConcurrency:   runtime.config.Backfill.ProviderConcurrency,
		CapabilityConcurrency: runtime.config.Backfill.CapabilityConcurrency,
		RecentWindow:          runtime.config.Backfill.RecentWindow,
		HistoryAgingStep:      runtime.config.Backfill.HistoryAgingStep,
	}
	clock := runtime.now
	runtime.mu.RUnlock()
	if coordinator == nil {
		return derivedBackfillCollectResult{}, nil
	}
	batchSize := policy.BatchSize
	if batchSize < 1 || batchSize > secretContinuationBatchSize {
		batchSize = secretContinuationBatchSize
	}
	var repositoryRow model.BackupRepository
	if err := runtime.db.WithContext(ctx).Select("id", "provider_kind").
		Where("id = ?", request.RepositoryID).First(&repositoryRow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return derivedBackfillCollectResult{}, fmt.Errorf("%w: rebuild repository", backupasset.ErrNotFound)
		}
		return derivedBackfillCollectResult{}, fmt.Errorf("load rebuild derived repository: %w", err)
	}
	var point model.RecoveryPoint
	if err := runtime.db.WithContext(ctx).
		Where("id = ? AND repository_id = ?", request.RecoveryPointID, request.RepositoryID).
		First(&point).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return derivedBackfillCollectResult{}, fmt.Errorf("%w: rebuild recovery point", backupasset.ErrNotFound)
		}
		return derivedBackfillCollectResult{}, fmt.Errorf("load rebuild derived point: %w", err)
	}
	advertisements, err := runtime.readyRebuildCapabilities(ctx)
	if err != nil {
		return derivedBackfillCollectResult{}, err
	}
	if len(advertisements) == 0 {
		return derivedBackfillCollectResult{}, nil
	}
	capabilities := make([]string, 0, len(advertisements))
	seenCapability := map[string]struct{}{}
	for _, advertisement := range advertisements {
		capability := strings.TrimSpace(advertisement.Capability)
		if capability == "" {
			continue
		}
		if _, exists := seenCapability[capability]; exists {
			continue
		}
		seenCapability[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	if len(capabilities) == 0 {
		return derivedBackfillCollectResult{}, nil
	}
	advertisementFingerprint := derivedBackfillAdvertisementFingerprint(capabilities, secretEnabled)
	revision, err := runtime.derivedBackfillFailedCanceledRevision(
		ctx, request.CatalogGenerationID, request.RecoveryPointID)
	if err != nil {
		return derivedBackfillCollectResult{}, err
	}
	walk := runtime.loadDerivedBackfillWalk(request.CatalogGenerationID, request.RecoveryPointID)
	if walk.advertisementFingerprint != advertisementFingerprint ||
		(walk.failedCanceledRevision != "" && walk.failedCanceledRevision != revision) {
		walk = derivedBackfillWalkState{}
	}
	if walk.complete {
		return derivedBackfillCollectResult{}, nil
	}
	startRevision := walk.failedCanceledRevision
	if startRevision == "" {
		startRevision = revision
	}
	provenCapabilityCount := runtime.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Select("COUNT(DISTINCT backup_asset_processing_jobs.capability)").
		Where("backup_asset_processing_jobs.entry_id = catalog_entries.entry_id").
		Where("backup_asset_processing_jobs.catalog_generation_id = catalog_entries.generation_id").
		Where("backup_asset_processing_jobs.recovery_point_id = catalog_entries.recovery_point_id").
		Where("backup_asset_processing_jobs.capability IN ?", capabilities).
		Where("backup_asset_processing_jobs.state NOT IN ?", []string{"failed", "canceled"})
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	now := clock().UTC()
	usage := processing.BackfillUsage{
		WindowStartedAt: now, ProviderActive: map[string]int{}, CapabilityActive: map[string]int{},
	}
	providerKind := strings.TrimSpace(repositoryRow.ProviderKind)
	if providerKind == "" {
		providerKind = "rebuild"
	}
	var descriptors []processing.WorkDescriptorV1
	afterEntryID := walk.afterEntryID
	inspected := 0
	// COUNT < advertised still selects rows whose leftover caps fail ValidateMedia.
	// Page past those until a real unproven pair is found, the inspect budget ends,
	// or candidates end. Persist the leftover cursor only after a zero-descriptor page.
	for inspected < inspectedLimit {
		pageSize := batchSize
		if remaining := inspectedLimit - inspected; remaining < pageSize {
			pageSize = remaining
		}
		var entries []model.CatalogEntry
		query := runtime.db.WithContext(ctx).
			Where("generation_id = ? AND recovery_point_id = ? AND entry_type = ?",
				request.CatalogGenerationID, request.RecoveryPointID, backupasset.CatalogEntryFile).
			Where("(?) < ?", provenCapabilityCount, len(capabilities))
		if afterEntryID != "" {
			query = query.Where("entry_id > ?", afterEntryID)
		}
		if err := query.Order("entry_id ASC").Limit(pageSize).Find(&entries).Error; err != nil {
			return derivedBackfillCollectResult{}, fmt.Errorf("load rebuild derived catalog entries: %w", err)
		}
		if len(entries) == 0 {
			if len(descriptors) == 0 {
				endRevision, err := runtime.derivedBackfillFailedCanceledRevision(
					ctx, request.CatalogGenerationID, request.RecoveryPointID)
				if err != nil {
					return derivedBackfillCollectResult{}, err
				}
				if endRevision != startRevision {
					runtime.persistDerivedBackfillWalk(request.CatalogGenerationID, request.RecoveryPointID, derivedBackfillWalkState{
						advertisementFingerprint: advertisementFingerprint,
						failedCanceledRevision:   endRevision,
					})
					return derivedBackfillCollectResult{incomplete: true}, nil
				}
				runtime.persistDerivedBackfillWalk(request.CatalogGenerationID, request.RecoveryPointID, derivedBackfillWalkState{
					afterEntryID: afterEntryID, complete: true, advertisementFingerprint: advertisementFingerprint,
					failedCanceledRevision: startRevision,
				})
			}
			return derivedBackfillCollectResult{descriptors: descriptors}, nil
		}
		inspected += len(entries)
		provenJobs, err := loadProvenRebuildDerivedJobs(ctx, runtime.db, request, entries, capabilities)
		if err != nil {
			return derivedBackfillCollectResult{}, err
		}
		for _, entry := range entries {
			for _, advertisement := range advertisements {
				profile, ok := capabilityspec.Lookup(advertisement.Capability, advertisement.OutputProfile, secretEnabled)
				if !ok || profile.ValidateMedia(entry.MimeType, entry.MimeType) != nil {
					continue
				}
				if _, exists := provenJobs[entry.EntryID+"\x00"+profile.Capability]; exists {
					continue
				}
				if applyAdmission {
					admission := processing.AdmitBackfill(policy, usage, processing.BackfillAdmissionRequest{
						PriorityClass: processing.PriorityBackground, Provider: providerKind,
						Capability: profile.Capability, EstimatedBytes: entry.Size,
					}, now)
					if !admission.Allowed {
						return derivedBackfillCollectResult{descriptors: descriptors}, nil
					}
				}
				descriptor := processing.WorkDescriptorV1{
					SchemaVersion: 1,
					Source: backupasset.AssetRef{
						RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID,
					},
					CatalogGenerationID:        request.CatalogGenerationID,
					SourceFingerprint:          point.SourceFingerprint,
					EntryFingerprint:           entry.Fingerprint,
					ProviderCapabilityRevision: int64(point.CapabilityRevision),
					Capability:                 profile.Capability,
					CapabilitySchema:           profile.CapabilitySchema,
					PipelineFingerprint:        advertisement.PipelineFingerprint,
					OutputProfile:              profile.OutputProfile,
					SecurityPolicyRevision:     processingSecurityPolicyRevision,
					Parameters:                 processing.CanonicalProductionParameters(profile),
				}
				if err := processing.ValidateProductionWorkDescriptorV1(descriptor, secretEnabled); err != nil {
					return derivedBackfillCollectResult{}, err
				}
				descriptors = append(descriptors, descriptor)
				usage.Jobs++
				usage.Bytes += entry.Size
				usage.ProviderActive[providerKind]++
				usage.CapabilityActive[profile.Capability]++
				if len(descriptors) >= batchSize {
					return derivedBackfillCollectResult{descriptors: descriptors}, nil
				}
			}
		}
		afterEntryID = entries[len(entries)-1].EntryID
	}
	if len(descriptors) == 0 {
		runtime.persistDerivedBackfillWalk(request.CatalogGenerationID, request.RecoveryPointID, derivedBackfillWalkState{
			afterEntryID: afterEntryID, advertisementFingerprint: advertisementFingerprint,
			failedCanceledRevision: startRevision,
		})
		return derivedBackfillCollectResult{incomplete: true}, nil
	}
	return derivedBackfillCollectResult{descriptors: descriptors}, nil
}

func loadProvenRebuildDerivedJobs(
	ctx context.Context,
	db *gorm.DB,
	request repository.DerivedBackfillRequest,
	entries []model.CatalogEntry,
	capabilities []string,
) (map[string]struct{}, error) {
	proven := map[string]struct{}{}
	if db == nil || len(entries) == 0 || len(capabilities) == 0 {
		return proven, nil
	}
	entryIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.EntryID)
	}
	var jobs []model.BackupAssetProcessingJob
	if err := db.WithContext(ctx).Select("entry_id", "capability").
		Where("catalog_generation_id = ? AND recovery_point_id = ? AND entry_id IN ? AND capability IN ? AND state NOT IN ?",
			request.CatalogGenerationID, request.RecoveryPointID, entryIDs, capabilities, []string{"failed", "canceled"}).
		Find(&jobs).Error; err != nil {
		return nil, fmt.Errorf("load rebuild derived jobs: %w", err)
	}
	for _, job := range jobs {
		proven[job.EntryID+"\x00"+job.Capability] = struct{}{}
	}
	return proven, nil
}

func (runtime *managedProcessingRuntime) readyRebuildCapabilities(ctx context.Context) ([]model.BackupAssetWorkerCapability, error) {
	var rows []model.BackupAssetWorkerCapability
	if err := runtime.db.WithContext(ctx).
		Table("backup_asset_worker_capabilities AS capabilities").
		Select("capabilities.*").
		Joins("JOIN backup_asset_worker_identities AS workers ON workers.id = capabilities.worker_id").
		Where("workers.trust_state = ? AND workers.health_state = ? AND capabilities.health_state = ?",
			"active", "ready", "ready").
		Order("capabilities.capability ASC, capabilities.output_profile ASC, capabilities.id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load rebuild derived capabilities: %w", err)
	}
	return rows, nil
}
