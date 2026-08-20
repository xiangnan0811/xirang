package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const maxRebuildPageSize = 200

type RebuildRequest struct {
	Limit  int
	Cursor string
}

type CatalogRebuildRequest struct {
	RepositoryID       string
	RecoveryPointID    string
	ManifestDigest     string
	CapabilityRevision int
}

type CatalogRebuildStart struct {
	GenerationID string
}

type CatalogRebuildStarter interface {
	StartFreshCatalogGeneration(context.Context, CatalogRebuildRequest) (CatalogRebuildStart, error)
}

type DerivedBackfillRequest struct {
	RepositoryID        string
	RecoveryPointID     string
	CatalogGenerationID string
	Priority            string
}

type DerivedBackfillQueuer interface {
	QueueLowPriorityDerivedBackfill(context.Context, DerivedBackfillRequest) (int, error)
}

type ExpectedDerivedDescriptor struct {
	EntryID    string
	Capability string
}

type DerivedExpectationSource interface {
	ExpectedDescriptors(context.Context, DerivedBackfillRequest) ([]ExpectedDerivedDescriptor, error)
}

type RebuildReason string

const (
	RebuildReasonInvalidManifest    RebuildReason = "invalid_manifest"
	RebuildReasonCatalogStartFailed RebuildReason = "catalog_start_failed"
	RebuildReasonDerivedQueueFailed RebuildReason = "derived_queue_failed"
)

type RebuildResult struct {
	Accepted       int                   `json:"accepted"`
	CatalogStarted int                   `json:"catalog_started"`
	DerivedQueued  int                   `json:"derived_queued"`
	Partial        int                   `json:"partial"`
	Failed         int                   `json:"failed"`
	Reasons        map[RebuildReason]int `json:"reasons"`
	NextCursor     string                `json:"next_cursor,omitempty"`
}

func (service *Service) RebuildAcceptedImports(
	ctx context.Context,
	repositoryID string,
	request RebuildRequest,
	requestContext RequestContext,
) (RebuildResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return RebuildResult{}, err
	}
	if err := requireImportAdmin(requestContext.Actor); err != nil {
		return RebuildResult{}, err
	}
	if service.db == nil || service.catalogRebuild == nil || service.derivedBackfill == nil {
		return RebuildResult{}, fmt.Errorf("%w: rebuild dependencies unavailable", backupasset.ErrInvalidState)
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil {
		return RebuildResult{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	if request.Limit < 0 || request.Limit > maxRebuildPageSize {
		return RebuildResult{}, fmt.Errorf("%w: invalid rebuild page limit", backupasset.ErrInvalidState)
	}
	limit := request.Limit
	if limit == 0 {
		limit = maxRebuildPageSize
	}
	repository, err := service.requireReadableRebuildAuthority(ctx, repositoryID)
	if err != nil {
		return RebuildResult{}, err
	}
	query := service.db.WithContext(ctx).Where("repository_id = ? AND review_state = ?", repositoryID, backupasset.ImportReviewAccepted)
	if request.Cursor != "" {
		if backupasset.ValidateOpaqueID(request.Cursor) != nil {
			return RebuildResult{}, fmt.Errorf("%w: invalid rebuild cursor", backupasset.ErrInvalidState)
		}
		var anchor model.BackupRepositoryImportCandidate
		if err := service.db.WithContext(ctx).Select("id", "created_at").
			Where("id = ? AND repository_id = ? AND review_state = ?", request.Cursor, repositoryID, backupasset.ImportReviewAccepted).
			First(&anchor).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return RebuildResult{}, fmt.Errorf("%w: invalid rebuild cursor", backupasset.ErrInvalidState)
		} else if err != nil {
			return RebuildResult{}, fmt.Errorf("load rebuild cursor: %w", err)
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
	}
	var candidates []model.BackupRepositoryImportCandidate
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&candidates).Error; err != nil {
		return RebuildResult{}, fmt.Errorf("list accepted import manifests: %w", err)
	}
	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}
	result := RebuildResult{Accepted: len(candidates), Reasons: make(map[RebuildReason]int)}
	if hasMore {
		result.NextCursor = candidates[len(candidates)-1].ID
	}
	for _, candidate := range candidates {
		service.rebuildAcceptedCandidate(ctx, repository, candidate, &result)
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeSuccess, repositoryID, nil, "rebuild", nil)
	return result, nil
}

func (service *Service) requireReadableRebuildAuthority(ctx context.Context, repositoryID string) (model.BackupRepository, error) {
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).Where("id = ?", repositoryID).First(&repository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BackupRepository{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	} else if err != nil {
		return model.BackupRepository{}, fmt.Errorf("load rebuild repository: %w", err)
	}
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&binding).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BackupRepository{}, fmt.Errorf("%w: active repository binding", backupasset.ErrConflict)
	} else if err != nil {
		return model.BackupRepository{}, fmt.Errorf("read rebuild binding: %w", err)
	}
	if strings.TrimSpace(binding.EncryptedConfig) == "" {
		return model.BackupRepository{}, fmt.Errorf("%w: unreadable repository binding", backupasset.ErrInvalidState)
	}
	return repository, nil
}

func (service *Service) ReconcileRebuilds(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil {
		return 0, fmt.Errorf("%w: repository rebuild reconciliation is unavailable", backupasset.ErrInvalidState)
	}
	if limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("%w: invalid rebuild reconciliation batch", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !service.db.Migrator().HasTable(&model.BackupRepositoryImportCandidate{}) {
		return 0, nil
	}
	if service.catalogRebuild == nil || service.derivedBackfill == nil {
		return 0, nil
	}
	started := 0
	inspected := 0
	if err := service.retryPendingDerived(ctx, &inspected, limit); err != nil {
		return started, err
	}
	if inspected >= limit {
		return started, nil
	}
	readable := make(map[string]model.BackupRepository)
	unreadable := make(map[string]bool)
	afterID := service.rebuildAfterID
	const reconcilePageSize = 50
	for inspected < limit {
		query := service.db.WithContext(ctx).Where("review_state = ?", backupasset.ImportReviewAccepted)
		if afterID != "" {
			query = query.Where("id > ?", afterID)
		}
		var candidates []model.BackupRepositoryImportCandidate
		if err := query.Order("id ASC").Limit(reconcilePageSize).Find(&candidates).Error; err != nil {
			return started, fmt.Errorf("reconcile accepted import rebuilds: %w", err)
		}
		if len(candidates) == 0 {
			service.rebuildAfterID = ""
			break
		}
		for _, candidate := range candidates {
			afterID = candidate.ID
			service.rebuildAfterID = afterID
			inspected++
			if unreadable[candidate.RepositoryID] {
				if inspected >= limit {
					return started, nil
				}
				continue
			}
			repository, ok := readable[candidate.RepositoryID]
			if !ok {
				loaded, err := service.requireReadableRebuildAuthority(ctx, candidate.RepositoryID)
				if err != nil {
					unreadable[candidate.RepositoryID] = true
					if inspected >= limit {
						return started, nil
					}
					continue
				}
				repository = loaded
				readable[candidate.RepositoryID] = repository
			}
			point, valid := service.validateAcceptedRebuildManifest(ctx, repository, candidate)
			if !valid {
				if inspected >= limit {
					return started, nil
				}
				continue
			}
			if request, ok := service.pendingDerivedFor(candidate.ID); ok {
				queued, queueErr := service.derivedBackfill.QueueLowPriorityDerivedBackfill(ctx, request)
				if queueErr == nil && queued >= 1 {
					service.clearPendingDerived(candidate.ID)
				}
				if inspected >= limit {
					return started, nil
				}
				continue
			}
			needed, err := service.catalogNeedsRebuild(ctx, point)
			if err != nil {
				return started, err
			}
			if needed {
				result := RebuildResult{Reasons: make(map[RebuildReason]int)}
				service.rebuildAcceptedCandidate(ctx, repository, candidate, &result)
				if result.CatalogStarted > 0 {
					started++
				}
			} else if generationID, ok := service.completeCatalogGenerationID(ctx, point); ok {
				if service.derivedBackfillUnproven(ctx, repository.ID, point.ID, generationID) {
					queued, queueErr := service.derivedBackfill.QueueLowPriorityDerivedBackfill(ctx, DerivedBackfillRequest{
						RepositoryID: repository.ID, RecoveryPointID: point.ID, CatalogGenerationID: generationID, Priority: "low",
					})
					if queueErr != nil || queued < 1 {
						service.rememberPendingDerived(candidate.ID, DerivedBackfillRequest{
							RepositoryID: repository.ID, RecoveryPointID: point.ID, CatalogGenerationID: generationID, Priority: "low",
						})
					}
				}
			}
			if inspected >= limit {
				return started, nil
			}
		}
		if len(candidates) < reconcilePageSize {
			service.rebuildAfterID = ""
			break
		}
	}
	return started, nil
}

func (service *Service) catalogNeedsRebuild(ctx context.Context, point model.RecoveryPoint) (bool, error) {
	if service.db == nil {
		return false, fmt.Errorf("%w: repository rebuild reconciliation is unavailable", backupasset.ErrInvalidState)
	}
	if !service.db.Migrator().HasTable(&model.CatalogGeneration{}) {
		return true, nil
	}
	var generations []model.CatalogGeneration
	if err := service.db.WithContext(ctx).Where("recovery_point_id = ?", point.ID).
		Order("generation DESC").Limit(12).Find(&generations).Error; err != nil {
		return false, fmt.Errorf("load catalog rebuild state: %w", err)
	}
	if len(generations) == 0 {
		return true, nil
	}
	for index := range generations {
		generation := generations[index]
		if generation.IsActive && generation.State == string(catalog.GenerationComplete) &&
			generation.SourceFingerprint == point.SourceFingerprint {
			return false, nil
		}
		if generation.State == string(catalog.GenerationBuilding) {
			return false, nil
		}
	}
	return true, nil
}

func (service *Service) rebuildAcceptedCandidate(
	ctx context.Context,
	repository model.BackupRepository,
	candidate model.BackupRepositoryImportCandidate,
	result *RebuildResult,
) {
	if result.Reasons == nil {
		result.Reasons = make(map[RebuildReason]int)
	}
	point, valid := service.validateAcceptedRebuildManifest(ctx, repository, candidate)
	if !valid {
		result.Failed++
		result.Reasons[RebuildReasonInvalidManifest]++
		return
	}
	started, err := service.catalogRebuild.StartFreshCatalogGeneration(ctx, CatalogRebuildRequest{
		RepositoryID: repository.ID, RecoveryPointID: point.ID, ManifestDigest: point.ManifestDigest,
		CapabilityRevision: point.CapabilityRevision,
	})
	if err != nil || backupasset.ValidateOpaqueID(started.GenerationID) != nil {
		result.Failed++
		result.Reasons[RebuildReasonCatalogStartFailed]++
		return
	}
	result.CatalogStarted++
	queued, err := service.derivedBackfill.QueueLowPriorityDerivedBackfill(ctx, DerivedBackfillRequest{
		RepositoryID: repository.ID, RecoveryPointID: point.ID, CatalogGenerationID: started.GenerationID, Priority: "low",
	})
	if err != nil || queued < 1 {
		service.rememberPendingDerived(candidate.ID, DerivedBackfillRequest{
			RepositoryID: repository.ID, RecoveryPointID: point.ID, CatalogGenerationID: started.GenerationID, Priority: "low",
		})
		result.Partial++
		result.Reasons[RebuildReasonDerivedQueueFailed]++
		return
	}
	result.DerivedQueued += queued
}

func (service *Service) rememberPendingDerived(candidateID string, request DerivedBackfillRequest) {
	if service == nil {
		return
	}
	service.pendingDerivedMu.Lock()
	defer service.pendingDerivedMu.Unlock()
	if service.pendingDerived == nil {
		service.pendingDerived = map[string]DerivedBackfillRequest{}
	}
	service.pendingDerived[candidateID] = request
}

func (service *Service) pendingDerivedFor(candidateID string) (DerivedBackfillRequest, bool) {
	if service == nil {
		return DerivedBackfillRequest{}, false
	}
	service.pendingDerivedMu.Lock()
	defer service.pendingDerivedMu.Unlock()
	request, ok := service.pendingDerived[candidateID]
	return request, ok
}

func (service *Service) retryPendingDerived(ctx context.Context, inspected *int, limit int) error {
	if service == nil || service.derivedBackfill == nil || inspected == nil {
		return nil
	}
	service.pendingDerivedMu.Lock()
	pending := make([]struct {
		id      string
		request DerivedBackfillRequest
	}, 0, len(service.pendingDerived))
	for id, request := range service.pendingDerived {
		pending = append(pending, struct {
			id      string
			request DerivedBackfillRequest
		}{id: id, request: request})
	}
	service.pendingDerivedMu.Unlock()
	sort.Slice(pending, func(left, right int) bool { return pending[left].id < pending[right].id })
	for _, item := range pending {
		if *inspected >= limit {
			return nil
		}
		*inspected++
		queued, err := service.derivedBackfill.QueueLowPriorityDerivedBackfill(ctx, item.request)
		if err == nil && queued >= 1 {
			service.clearPendingDerived(item.id)
		}
	}
	return nil
}

func (service *Service) clearPendingDerived(candidateID string) {
	if service == nil {
		return
	}
	service.pendingDerivedMu.Lock()
	defer service.pendingDerivedMu.Unlock()
	delete(service.pendingDerived, candidateID)
}

func (service *Service) completeCatalogGenerationID(ctx context.Context, point model.RecoveryPoint) (string, bool) {
	if service.db == nil || !service.db.Migrator().HasTable(&model.CatalogGeneration{}) {
		return "", false
	}
	var generation model.CatalogGeneration
	err := service.db.WithContext(ctx).Where(
		"recovery_point_id = ? AND is_active = ? AND state = ? AND source_fingerprint = ?",
		point.ID, true, string(catalog.GenerationComplete), point.SourceFingerprint,
	).Order("generation DESC").Limit(1).Find(&generation).Error
	if err != nil || backupasset.ValidateOpaqueID(generation.ID) != nil {
		return "", false
	}
	return generation.ID, true
}

func (service *Service) derivedBackfillUnproven(ctx context.Context, repositoryID, recoveryPointID, generationID string) bool {
	if service.db == nil || !service.db.Migrator().HasTable(&model.BackupAssetProcessingJob{}) {
		return false
	}
	if service.derivedExpectations != nil {
		expected, err := service.derivedExpectations.ExpectedDescriptors(ctx, DerivedBackfillRequest{
			RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, CatalogGenerationID: generationID, Priority: "low",
		})
		if err != nil {
			return true
		}
		if len(expected) == 0 {
			return false
		}
		var jobs []model.BackupAssetProcessingJob
		if err := service.db.WithContext(ctx).Select("entry_id", "capability").
			Where("recovery_point_id = ? AND catalog_generation_id = ? AND state NOT IN ?",
				recoveryPointID, generationID, []string{"failed", "canceled"}).
			Find(&jobs).Error; err != nil {
			return true
		}
		seen := make(map[string]struct{}, len(jobs))
		for _, job := range jobs {
			seen[job.EntryID+"\x00"+job.Capability] = struct{}{}
		}
		for _, descriptor := range expected {
			if _, ok := seen[descriptor.EntryID+"\x00"+descriptor.Capability]; !ok {
				return true
			}
		}
		return false
	}
	var count int64
	if err := service.db.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
		Where("recovery_point_id = ? AND catalog_generation_id = ? AND state NOT IN ?",
			recoveryPointID, generationID, []string{"failed", "canceled"}).
		Count(&count).Error; err != nil {
		return true
	}
	return count == 0
}

func (service *Service) validateAcceptedRebuildManifest(
	ctx context.Context,
	repository model.BackupRepository,
	candidate model.BackupRepositoryImportCandidate,
) (model.RecoveryPoint, bool) {
	_, evidence, err := validateStoredImportCandidate(repository, candidate)
	if err != nil || candidate.ReviewState != string(backupasset.ImportReviewAccepted) || candidate.ReviewedBy == nil ||
		*candidate.ReviewedBy == 0 || candidate.ReviewedAt == nil || candidate.AcceptedRecoveryPointID == nil ||
		backupasset.ValidateOpaqueID(*candidate.AcceptedRecoveryPointID) != nil {
		return model.RecoveryPoint{}, false
	}
	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).Where("id = ? AND repository_id = ?", *candidate.AcceptedRecoveryPointID, repository.ID).First(&point).Error; err != nil {
		return model.RecoveryPoint{}, false
	}
	expectedSemantics := backupasset.PointVersionSemantics(candidate.CandidateKind)
	if candidate.CandidateKind == string(backupasset.ImportCandidateMutableHead) {
		expectedSemantics = backupasset.PointImportedBaseline
	}
	if point.Semantics != string(expectedSemantics) || point.State != string(backupasset.RecoveryPointCommitted) ||
		point.SourceFingerprint != candidate.SourceFingerprint || point.EncryptedProviderLocator != candidate.EncryptedProviderLocator ||
		point.ManifestDigestAlgorithm != "sha256" || point.ManifestDigest != evidence.OpaqueDigest || point.CapturedAt == nil ||
		!point.CapturedAt.Equal(evidence.CapturedAt) || point.CapabilityRevision < 1 || point.CapabilityRevision > repository.CapabilityRevision ||
		point.CapabilitiesJSON == "" || point.ImmutabilityLevel == string(backupasset.ImmutabilityMutable) {
		return model.RecoveryPoint{}, false
	}
	return point, true
}
