package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	defaultCatalogPageLimit = 100
	maxCatalogPageLimit     = 200
	maxBreadcrumbDepth      = 256
)

type ServiceDependencies struct {
	DB                *gorm.DB
	Ownership         *Ownership
	Cursor            *CursorCodec
	Now               func() time.Time
	ReconcileInterval time.Duration
	FeatureEnabled    func() (bool, error)
}

type Service struct {
	db                *gorm.DB
	ownership         *Ownership
	cursor            *CursorCodec
	now               func() time.Time
	reconcileInterval time.Duration
	featureEnabled    func() (bool, error)
}

type RecoveryPointListRequest struct {
	Limit  int
	Cursor string
	Sort   RecoveryPointSort
}

type RecoveryPointPage struct {
	Items      []RecoveryPointView `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type RecoveryPointView struct {
	backupasset.RecoveryPointDTO
	ProducingTaskName string    `json:"producing_task_name"`
	ProducingNodeID   uint      `json:"producing_node_id"`
	ProducingNodeName string    `json:"producing_node_name"`
	Catalog           StatusDTO `json:"catalog"`
}

type RepositorySummaryDTO struct {
	RecoveryPointCount   int64                  `json:"recovery_point_count"`
	CompleteCatalogCount int64                  `json:"complete_catalog_count"`
	Coverage             CoverageStatus         `json:"coverage"`
	ContentAvailability  ContentAvailabilityDTO `json:"content_availability"`
	Permissions          PermissionsDTO         `json:"permissions"`
}

type EntryListRequest struct {
	ParentEntryID string
	Limit         int
	Cursor        string
	Sort          EntrySort
}

type EntryPage struct {
	Items      []EntryDTO `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

type recoveryPointSortControl struct {
	ID          string
	CapturedAt  *time.Time
	CommittedAt *time.Time
	CreatedAt   time.Time
}

func NewService(dependencies ServiceDependencies) (*Service, error) {
	if dependencies.DB == nil || dependencies.Ownership == nil || dependencies.Cursor == nil ||
		dependencies.ReconcileInterval <= 0 {
		return nil, fmt.Errorf("%w: invalid Catalog service dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		db: dependencies.DB, ownership: dependencies.Ownership, cursor: dependencies.Cursor,
		now: dependencies.Now, reconcileInterval: dependencies.ReconcileInterval,
		featureEnabled: dependencies.FeatureEnabled,
	}, nil
}

func (service *Service) RepositorySummary(
	ctx context.Context,
	repositoryID string,
	scope AuthorizationScope,
) (RepositorySummaryDTO, error) {
	if err := service.validateRequestScope(repositoryID, scope); err != nil {
		return RepositorySummaryDTO{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var candidateIDs []string
	if err := service.db.WithContext(ctx).Table("recovery_points AS points").Select("points.id").
		Where("points.repository_id = ?", repositoryID).
		Where(`((points.semantics = ? AND points.state = ?) OR
			(points.semantics IN ? AND points.state IN ?))`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)},
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Order("points.id ASC").Limit(maxOwnershipCandidateIDs + 1).Scan(&candidateIDs).Error; err != nil {
		return RepositorySummaryDTO{}, fmt.Errorf("load repository Catalog summary controls: %w", err)
	}
	if len(candidateIDs) > maxOwnershipCandidateIDs {
		return RepositorySummaryDTO{}, fmt.Errorf("%w: repository Catalog summary", ErrOwnershipProjectionLimit)
	}
	authorizedIDs, err := service.ownership.AuthorizedPointIDs(ctx, scope, candidateIDs)
	if err != nil {
		return RepositorySummaryDTO{}, err
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).Select("id", "status").Where("id = ?", repositoryID).Take(&repository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return RepositorySummaryDTO{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	} else if err != nil {
		return RepositorySummaryDTO{}, fmt.Errorf("load repository Catalog summary status: %w", err)
	}
	result := RepositorySummaryDTO{
		RecoveryPointCount: int64(len(authorizedIDs)), Coverage: CoverageUnavailable,
		Permissions: PermissionsDTO{List: true},
	}
	if repository.Status == string(backupasset.RepositoryOnline) {
		result.ContentAvailability.Available = true
	} else {
		code := backupasset.CapabilityProviderUnavailable
		if repository.Status == string(backupasset.RepositoryOffline) {
			code = backupasset.CapabilityRepositoryOffline
		} else if repository.Status == string(backupasset.RepositoryDisconnected) {
			code = backupasset.CapabilityRepositoryDisconnected
		}
		result.ContentAvailability.Reason = &backupasset.CapabilityReason{Code: code}
	}
	for _, pointID := range authorizedIDs {
		point, _, loadErr := service.loadPointAndRepository(ctx, pointID)
		if loadErr != nil {
			return RepositorySummaryDTO{}, loadErr
		}
		status, statusErr := service.projectStatus(ctx, point, repository)
		if statusErr != nil {
			return RepositorySummaryDTO{}, statusErr
		}
		if status.Coverage.Status == CoverageComplete {
			result.CompleteCatalogCount++
		}
	}
	if result.RecoveryPointCount > 0 {
		switch {
		case result.CompleteCatalogCount == result.RecoveryPointCount:
			result.Coverage = CoverageComplete
		case result.CompleteCatalogCount > 0:
			result.Coverage = CoveragePartial
		default:
			result.Coverage = CoverageUnavailable
		}
	}
	return result, nil
}

func (service *Service) ListRecoveryPoints(
	ctx context.Context,
	repositoryID string,
	scope AuthorizationScope,
	request RecoveryPointListRequest,
) (RecoveryPointPage, error) {
	if err := service.validateRequestScope(repositoryID, scope); err != nil {
		return RecoveryPointPage{}, err
	}
	limit, err := normalizeCatalogPageLimit(request.Limit)
	if err != nil {
		return RecoveryPointPage{}, err
	}
	if request.Sort == "" {
		request.Sort = RecoveryPointSortCapturedDesc
	}
	if !validRecoveryPointSort(request.Sort) {
		return RecoveryPointPage{}, fmt.Errorf("%w: recovery point sort", ErrInvalidCatalogContract)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cursorScope := CursorScope{
		Endpoint: CursorEndpointRecoveryPoints, Direction: CursorForward, UserID: scope.UserID, Role: scope.Role,
		Sort: string(request.Sort), RepositoryID: repositoryID,
	}
	var anchor *recoveryPointSortControl
	if strings.TrimSpace(request.Cursor) != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return RecoveryPointPage{}, decodeErr
		}
		visible, authorizeErr := service.ownership.AuthorizedPointIDs(ctx, scope, []string{decoded.Anchor.RecoveryPointID})
		if authorizeErr != nil {
			return RecoveryPointPage{}, authorizeErr
		}
		if len(visible) != 1 {
			return RecoveryPointPage{}, fmt.Errorf("%w: recovery point cursor anchor", ErrStaleCursor)
		}
		loaded, loadErr := service.loadRecoveryPointSortControl(ctx, repositoryID, visible[0])
		if loadErr != nil {
			return RecoveryPointPage{}, loadErr
		}
		anchor = &loaded
	}

	scanBudget := min(maxOwnershipCandidateIDs, max(200, limit*20))
	visibleIDs := make([]string, 0, limit+1)
	scanned := 0
	hasCandidateMore := true
	for len(visibleIDs) < limit+1 && hasCandidateMore {
		remaining := scanBudget - scanned
		if remaining <= 0 {
			return RecoveryPointPage{}, fmt.Errorf("%w: recovery point scan budget", ErrOwnershipProjectionLimit)
		}
		chunkSize := min(remaining, max(50, limit*2))
		candidates, more, queryErr := service.listRecoveryPointControls(ctx, repositoryID, request.Sort, anchor, chunkSize)
		if queryErr != nil {
			return RecoveryPointPage{}, queryErr
		}
		hasCandidateMore = more
		if len(candidates) == 0 {
			break
		}
		scanned += len(candidates)
		candidateIDs := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			candidateIDs = append(candidateIDs, candidate.ID)
		}
		authorized, authorizeErr := service.ownership.AuthorizedPointIDs(ctx, scope, candidateIDs)
		if authorizeErr != nil {
			return RecoveryPointPage{}, authorizeErr
		}
		visibleIDs = append(visibleIDs, authorized...)
		last := candidates[len(candidates)-1]
		anchor = &last
		if scanned >= scanBudget && hasCandidateMore && len(visibleIDs) < limit+1 {
			return RecoveryPointPage{}, fmt.Errorf("%w: recovery point scan budget", ErrOwnershipProjectionLimit)
		}
	}
	hasMore := len(visibleIDs) > limit
	if hasMore {
		visibleIDs = visibleIDs[:limit]
	}
	page := RecoveryPointPage{Items: make([]RecoveryPointView, 0, len(visibleIDs))}
	for _, pointID := range visibleIDs {
		view, viewErr := service.loadAuthorizedRecoveryPoint(ctx, pointID)
		if viewErr != nil {
			return RecoveryPointPage{}, viewErr
		}
		page.Items = append(page.Items, view)
	}
	if hasMore && len(visibleIDs) > 0 {
		cursorScope.Anchor.RecoveryPointID = visibleIDs[len(visibleIDs)-1]
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return RecoveryPointPage{}, err
		}
	}
	return page, nil
}

func (service *Service) GetRecoveryPoint(ctx context.Context, pointID string, scope AuthorizationScope) (RecoveryPointView, error) {
	if err := service.validatePointScope(pointID, scope); err != nil {
		return RecoveryPointView{}, err
	}
	visible, err := service.ownership.AuthorizedPointIDs(ctx, scope, []string{pointID})
	if err != nil {
		return RecoveryPointView{}, err
	}
	if len(visible) != 1 {
		return RecoveryPointView{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
	}
	return service.loadAuthorizedRecoveryPoint(ctx, pointID)
}

func (service *Service) GetCatalogStatus(ctx context.Context, pointID string, scope AuthorizationScope) (StatusDTO, error) {
	view, err := service.GetRecoveryPoint(ctx, pointID, scope)
	if err != nil {
		return StatusDTO{}, err
	}
	return view.Catalog, nil
}

func (service *Service) ListEntries(
	ctx context.Context,
	pointID string,
	scope AuthorizationScope,
	request EntryListRequest,
) (EntryPage, error) {
	point, repository, generation, err := service.authorizedActivePoint(ctx, pointID, scope)
	if err != nil {
		return EntryPage{}, err
	}
	limit, err := normalizeCatalogPageLimit(request.Limit)
	if err != nil {
		return EntryPage{}, err
	}
	if request.Sort == "" {
		request.Sort = EntrySortNameAsc
	}
	if !validEntrySort(request.Sort) {
		return EntryPage{}, fmt.Errorf("%w: entry sort", ErrInvalidCatalogContract)
	}
	if request.ParentEntryID != "" {
		if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: request.ParentEntryID}) != nil {
			return EntryPage{}, fmt.Errorf("%w: parent entry", backupasset.ErrNotFound)
		}
		parent, parentErr := service.loadCatalogEntry(ctx, generation.ID, point.ID, request.ParentEntryID)
		if parentErr != nil || parent.EntryType != string(backupasset.CatalogEntryDirectory) {
			return EntryPage{}, fmt.Errorf("%w: parent entry", backupasset.ErrNotFound)
		}
	}
	cursorScope := CursorScope{
		Endpoint: CursorEndpointEntries, Direction: CursorForward, UserID: scope.UserID, Role: scope.Role,
		Sort: string(request.Sort), RepositoryID: repository.ID, RecoveryPointID: point.ID,
		GenerationID: generation.ID, ParentEntryID: request.ParentEntryID,
	}
	var anchor *model.CatalogEntry
	if strings.TrimSpace(request.Cursor) != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return EntryPage{}, decodeErr
		}
		loaded, loadErr := service.loadCatalogEntry(ctx, generation.ID, point.ID, decoded.Anchor.EntryID)
		if loadErr != nil || !sameOptionalString(loaded.ParentEntryID, request.ParentEntryID) {
			return EntryPage{}, fmt.Errorf("%w: entry cursor anchor", ErrStaleCursor)
		}
		anchor = &loaded
	}
	query := service.db.WithContext(ctx).Where("generation_id = ? AND recovery_point_id = ?", generation.ID, point.ID)
	if request.ParentEntryID == "" {
		query = query.Where("parent_entry_id IS NULL")
	} else {
		query = query.Where("parent_entry_id = ?", request.ParentEntryID)
	}
	query = service.applyEntrySort(query, request.Sort, anchor)
	var rows []model.CatalogEntry
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
		return EntryPage{}, fmt.Errorf("list Catalog entries: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := EntryPage{Items: make([]EntryDTO, 0, len(rows))}
	for _, row := range rows {
		dto, mapErr := catalogEntryDTO(row, false)
		if mapErr != nil {
			return EntryPage{}, mapErr
		}
		page.Items = append(page.Items, dto)
	}
	if hasMore && len(rows) > 0 {
		cursorScope.Anchor.EntryID = rows[len(rows)-1].EntryID
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return EntryPage{}, err
		}
	}
	return page, nil
}

func (service *Service) GetEntry(ctx context.Context, pointID, entryID string, scope AuthorizationScope) (EntryDTO, error) {
	if backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) != nil {
		return EntryDTO{}, fmt.Errorf("%w: Catalog entry", backupasset.ErrNotFound)
	}
	point, _, generation, err := service.authorizedActivePoint(ctx, pointID, scope)
	if err != nil {
		return EntryDTO{}, err
	}
	entry, err := service.loadCatalogEntry(ctx, generation.ID, point.ID, entryID)
	if err != nil {
		return EntryDTO{}, fmt.Errorf("%w: Catalog entry", backupasset.ErrNotFound)
	}
	dto, err := catalogEntryDTO(entry, true)
	if err != nil {
		return EntryDTO{}, err
	}
	breadcrumb, err := service.loadBreadcrumb(ctx, generation.ID, point.ID, entry.ParentEntryID)
	if err != nil {
		return EntryDTO{}, err
	}
	dto.Breadcrumb = breadcrumb
	return dto, nil
}

func (service *Service) authorizedActivePoint(
	ctx context.Context,
	pointID string,
	scope AuthorizationScope,
) (model.RecoveryPoint, model.BackupRepository, model.CatalogGeneration, error) {
	if err := service.validatePointScope(pointID, scope); err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, err
	}
	visible, err := service.ownership.AuthorizedPointIDs(ctx, scope, []string{pointID})
	if err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, err
	}
	if len(visible) != 1 {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
	}
	point, repository, err := service.loadPointAndRepository(ctx, pointID)
	if err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, err
	}
	if !publicCatalogPoint(point) {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
	}
	var generation model.CatalogGeneration
	err = service.db.WithContext(ctx).Where("recovery_point_id = ? AND state = ? AND is_active = ?", point.ID, GenerationComplete, true).
		First(&generation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, fmt.Errorf("%w: active generation", ErrCatalogUnavailable)
	}
	if err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, model.CatalogGeneration{}, fmt.Errorf("load active Catalog generation: %w", err)
	}
	return point, repository, generation, nil
}

func (service *Service) loadAuthorizedRecoveryPoint(ctx context.Context, pointID string) (RecoveryPointView, error) {
	point, repository, err := service.loadPointAndRepository(ctx, pointID)
	if err != nil {
		return RecoveryPointView{}, err
	}
	if !publicCatalogPoint(point) {
		return RecoveryPointView{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
	}
	dto, err := backupasset.ToRecoveryPointDTO(point, backupasset.VersionMode(repository.VersionMode))
	if err != nil {
		return RecoveryPointView{}, fmt.Errorf("project Catalog recovery point: %w", err)
	}
	status, err := service.projectStatus(ctx, point, repository)
	if err != nil {
		return RecoveryPointView{}, err
	}
	return RecoveryPointView{
		RecoveryPointDTO: dto, ProducingTaskName: point.ProducingTaskNameSnapshot,
		ProducingNodeID: point.ProducingNodeIDSnapshot, ProducingNodeName: point.ProducingNodeNameSnapshot,
		Catalog: status,
	}, nil
}

func (service *Service) loadPointAndRepository(ctx context.Context, pointID string) (model.RecoveryPoint, model.BackupRepository, error) {
	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).First(&point, "id = ?", pointID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return model.RecoveryPoint{}, model.BackupRepository{}, fmt.Errorf("%w: recovery point", backupasset.ErrNotFound)
	} else if err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, fmt.Errorf("load Catalog recovery point: %w", err)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
		return model.RecoveryPoint{}, model.BackupRepository{}, fmt.Errorf("load Catalog repository: %w", err)
	}
	return point, repository, nil
}

func (service *Service) projectStatus(ctx context.Context, point model.RecoveryPoint, repository model.BackupRepository) (StatusDTO, error) {
	var active *model.CatalogGeneration
	var latest *model.CatalogGeneration
	var activeRow model.CatalogGeneration
	if err := service.db.WithContext(ctx).Where("recovery_point_id = ? AND is_active = ?", point.ID, true).
		Take(&activeRow).Error; err == nil {
		active = &activeRow
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StatusDTO{}, fmt.Errorf("load active Catalog generation: %w", err)
	}
	var latestRow model.CatalogGeneration
	if err := service.db.WithContext(ctx).Where("recovery_point_id = ?", point.ID).
		Order("generation DESC, id DESC").Take(&latestRow).Error; err == nil {
		latest = &latestRow
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return StatusDTO{}, fmt.Errorf("load latest Catalog generation: %w", err)
	}
	status := StatusDTO{
		Coverage:            CoverageDTO{Status: CoverageUnavailable},
		Staleness:           StalenessDTO{Status: StalenessUnknown},
		ContentAvailability: contentAvailability(point, repository),
		Permissions:         PermissionsDTO{List: true},
	}
	if active != nil {
		dto, err := generationDTO(*active)
		if err != nil {
			return StatusDTO{}, err
		}
		if dto.State != GenerationComplete {
			return StatusDTO{}, fmt.Errorf("%w: active generation state", ErrUnknownInternalState)
		}
		status.Generation = &dto
		status.Coverage.Status = CoverageComplete
		status.Coverage.IndexedEntries = active.WrittenEntryCount
		status.Coverage.ManifestDigest = active.ExpectedDigest
		status.Coverage.ObservedAt = generationObservedAt(*active)
		if active.ManifestID != nil {
			expected := active.ExpectedEntryCount
			status.Coverage.ExpectedEntries = &expected
		}
		status.Staleness = service.projectStaleness(point, *active)
	}
	if latest != nil && (active == nil || latest.ID != active.ID) {
		dto, err := generationDTO(*latest)
		if err != nil {
			return StatusDTO{}, err
		}
		status.LatestBuild = &dto
		if active == nil {
			status.Coverage.Status = coverageFromGeneration(dto.State)
			status.Coverage.IndexedEntries = latest.WrittenEntryCount
			status.Coverage.ObservedAt = generationObservedAt(*latest)
		}
	}
	if err := status.Validate(); err != nil {
		return StatusDTO{}, err
	}
	return status, nil
}

func (service *Service) projectStaleness(point model.RecoveryPoint, generation model.CatalogGeneration) StalenessDTO {
	if backupasset.PointVersionSemantics(point.Semantics) != backupasset.PointMutableHead {
		return StalenessDTO{Status: StalenessFresh, ObservedAt: utcPointer(point.CommittedAt)}
	}
	if point.ObservedAt == nil || point.ObservedAt.IsZero() {
		return StalenessDTO{Status: StalenessUnknown}
	}
	observed := point.ObservedAt.UTC()
	if generation.SourceFingerprint != point.SourceFingerprint || !service.utcNow().Before(observed.Add(2*service.reconcileInterval)) {
		return StalenessDTO{
			Status: StalenessStale, ObservedAt: &observed,
			Reason: &backupasset.CapabilityReason{Code: backupasset.CapabilityMutableSourceChanged},
		}
	}
	return StalenessDTO{Status: StalenessFresh, ObservedAt: &observed}
}

func contentAvailability(point model.RecoveryPoint, repository model.BackupRepository) ContentAvailabilityDTO {
	if backupasset.RepositoryStatus(repository.Status) == backupasset.RepositoryOnline &&
		backupasset.PhysicalAvailability(point.PhysicalAvailability) == backupasset.PhysicalOnline {
		return ContentAvailabilityDTO{Available: true}
	}
	code := backupasset.CapabilityProviderUnavailable
	switch backupasset.RepositoryStatus(repository.Status) {
	case backupasset.RepositoryOffline:
		code = backupasset.CapabilityRepositoryOffline
	case backupasset.RepositoryDisconnected:
		code = backupasset.CapabilityRepositoryDisconnected
	}
	return ContentAvailabilityDTO{Reason: &backupasset.CapabilityReason{Code: code}}
}

func generationDTO(generation model.CatalogGeneration) (GenerationDTO, error) {
	state, err := ParseGenerationState(generation.State)
	if err != nil {
		return GenerationDTO{}, err
	}
	errorCode, err := ParseGenerationErrorCode(generation.ErrorCode)
	if err != nil {
		return GenerationDTO{}, err
	}
	dto := GenerationDTO{
		ID: generation.ID, Sequence: generation.Generation, State: state, StartedAt: generation.StartedAt.UTC(),
		FinishedAt: utcPointer(generation.FinishedAt), ErrorCode: errorCode, CorrelationID: generation.CorrelationID,
	}
	if err := dto.validate(); err != nil {
		return GenerationDTO{}, err
	}
	return dto, nil
}

func coverageFromGeneration(state GenerationState) CoverageStatus {
	switch state {
	case GenerationBuilding:
		return CoverageBuilding
	case GenerationPartial:
		return CoveragePartial
	case GenerationFailed:
		return CoverageFailed
	default:
		return CoverageUnavailable
	}
}

func generationObservedAt(generation model.CatalogGeneration) time.Time {
	if generation.FinishedAt != nil {
		return generation.FinishedAt.UTC()
	}
	return generation.StartedAt.UTC()
}

func (service *Service) listRecoveryPointControls(
	ctx context.Context,
	repositoryID string,
	sort RecoveryPointSort,
	anchor *recoveryPointSortControl,
	limit int,
) ([]recoveryPointSortControl, bool, error) {
	query := service.db.WithContext(ctx).Table("recovery_points AS points").
		Select("points.id", "points.captured_at", "points.committed_at", "points.created_at").
		Where("points.repository_id = ?", repositoryID).
		Where(`((points.semantics = ? AND points.state = ?) OR
			(points.semantics IN ? AND points.state IN ?))`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)},
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)})
	if anchor != nil {
		anchorTime := recoveryPointSortTime(*anchor, sort)
		switch sort {
		case RecoveryPointSortCapturedDesc:
			query = query.Where(`COALESCE(points.captured_at, points.committed_at, points.created_at) < ? OR
				(COALESCE(points.captured_at, points.committed_at, points.created_at) = ? AND points.id < ?)`, anchorTime, anchorTime, anchor.ID)
		case RecoveryPointSortCapturedAsc:
			query = query.Where(`COALESCE(points.captured_at, points.committed_at, points.created_at) > ? OR
				(COALESCE(points.captured_at, points.committed_at, points.created_at) = ? AND points.id > ?)`, anchorTime, anchorTime, anchor.ID)
		case RecoveryPointSortCreatedDesc:
			query = query.Where("points.created_at < ? OR (points.created_at = ? AND points.id < ?)", anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
		}
	}
	switch sort {
	case RecoveryPointSortCapturedDesc:
		query = query.Order("COALESCE(points.captured_at, points.committed_at, points.created_at) DESC, points.id DESC")
	case RecoveryPointSortCapturedAsc:
		query = query.Order("COALESCE(points.captured_at, points.committed_at, points.created_at) ASC, points.id ASC")
	case RecoveryPointSortCreatedDesc:
		query = query.Order("points.created_at DESC, points.id DESC")
	}
	var rows []recoveryPointSortControl
	if err := query.Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("list Catalog recovery point controls: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	return rows, hasMore, nil
}

func (service *Service) loadRecoveryPointSortControl(ctx context.Context, repositoryID, pointID string) (recoveryPointSortControl, error) {
	var control recoveryPointSortControl
	if err := service.db.WithContext(ctx).Table("recovery_points AS points").
		Select("points.id", "points.captured_at", "points.committed_at", "points.created_at").
		Where("points.id = ? AND points.repository_id = ?", pointID, repositoryID).Take(&control).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return recoveryPointSortControl{}, fmt.Errorf("%w: recovery point cursor anchor", ErrStaleCursor)
	} else if err != nil {
		return recoveryPointSortControl{}, fmt.Errorf("load recovery point cursor anchor: %w", err)
	}
	return control, nil
}

func recoveryPointSortTime(control recoveryPointSortControl, sort RecoveryPointSort) time.Time {
	if sort == RecoveryPointSortCreatedDesc {
		return control.CreatedAt.UTC()
	}
	if control.CapturedAt != nil {
		return control.CapturedAt.UTC()
	}
	if control.CommittedAt != nil {
		return control.CommittedAt.UTC()
	}
	return control.CreatedAt.UTC()
}

func (service *Service) applyEntrySort(query *gorm.DB, sort EntrySort, anchor *model.CatalogEntry) *gorm.DB {
	name := "name COLLATE BINARY"
	if service.db.Name() == "postgres" {
		name = `name COLLATE "C"`
	}
	if anchor != nil {
		switch sort {
		case EntrySortNameAsc:
			query = query.Where(fmt.Sprintf("(%s > ?) OR (%s = ? AND entry_id > ?)", name, name), anchor.Name, anchor.Name, anchor.EntryID)
		case EntrySortNameDesc:
			query = query.Where(fmt.Sprintf("(%s < ?) OR (%s = ? AND entry_id < ?)", name, name), anchor.Name, anchor.Name, anchor.EntryID)
		case EntrySortSizeDesc:
			query = query.Where(fmt.Sprintf("size < ? OR (size = ? AND ((%s > ?) OR (%s = ? AND entry_id > ?)))", name, name),
				anchor.Size, anchor.Size, anchor.Name, anchor.Name, anchor.EntryID)
		case EntrySortModifiedDesc:
			if anchor.ModifiedAt == nil {
				query = query.Where(fmt.Sprintf("modified_at IS NULL AND ((%s > ?) OR (%s = ? AND entry_id > ?))", name, name),
					anchor.Name, anchor.Name, anchor.EntryID)
			} else {
				query = query.Where(fmt.Sprintf(`modified_at IS NULL OR modified_at < ? OR
					(modified_at = ? AND ((%s > ?) OR (%s = ? AND entry_id > ?)))`, name, name),
					anchor.ModifiedAt, anchor.ModifiedAt, anchor.Name, anchor.Name, anchor.EntryID)
			}
		}
	}
	switch sort {
	case EntrySortNameAsc:
		return query.Order(name + " ASC, entry_id ASC")
	case EntrySortNameDesc:
		return query.Order(name + " DESC, entry_id DESC")
	case EntrySortSizeDesc:
		return query.Order("size DESC, " + name + " ASC, entry_id ASC")
	case EntrySortModifiedDesc:
		return query.Order("modified_at IS NULL ASC, modified_at DESC, " + name + " ASC, entry_id ASC")
	default:
		return query
	}
}

func (service *Service) loadCatalogEntry(ctx context.Context, generationID, pointID, entryID string) (model.CatalogEntry, error) {
	var entry model.CatalogEntry
	err := service.db.WithContext(ctx).Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generationID, pointID, entryID).
		Take(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CatalogEntry{}, fmt.Errorf("%w: Catalog entry", backupasset.ErrNotFound)
	}
	if err != nil {
		return model.CatalogEntry{}, fmt.Errorf("load Catalog entry: %w", err)
	}
	return entry, nil
}

func (service *Service) loadBreadcrumb(ctx context.Context, generationID, pointID string, parentID *string) ([]BreadcrumbDTO, error) {
	reversed := make([]BreadcrumbDTO, 0)
	seen := make(map[string]struct{})
	for parentID != nil {
		if len(reversed) >= maxBreadcrumbDepth {
			return nil, fmt.Errorf("%w: Catalog breadcrumb depth", ErrInvalidCatalogContract)
		}
		if _, exists := seen[*parentID]; exists {
			return nil, fmt.Errorf("%w: Catalog breadcrumb cycle", ErrInvalidCatalogContract)
		}
		seen[*parentID] = struct{}{}
		entry, err := service.loadCatalogEntry(ctx, generationID, pointID, *parentID)
		if err != nil {
			return nil, err
		}
		reversed = append(reversed, BreadcrumbDTO{RecoveryPointID: pointID, EntryID: entry.EntryID, Name: entry.Name})
		parentID = entry.ParentEntryID
	}
	result := make([]BreadcrumbDTO, len(reversed))
	for index := range reversed {
		result[len(reversed)-1-index] = reversed[index]
	}
	return result, nil
}

func catalogEntryDTO(entry model.CatalogEntry, includeBreadcrumb bool) (EntryDTO, error) {
	entryType := backupasset.CatalogEntryType(entry.EntryType)
	if !validCatalogEntryType(entryType) {
		return EntryDTO{}, fmt.Errorf("%w: Catalog entry type", ErrUnknownInternalState)
	}
	strength, err := ParseFingerprintStrength(entry.FingerprintStrength)
	if err != nil {
		return EntryDTO{}, err
	}
	dto := EntryDTO{
		RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID, ParentEntryID: entry.ParentEntryID,
		Name: entry.Name, EntryType: entryType, Size: entry.Size, ModifiedAt: utcPointer(entry.ModifiedAt),
		Mode: entry.Mode, Owner: entry.Owner, MIMEType: entry.MimeType, FingerprintStrength: strength,
	}
	if includeBreadcrumb {
		dto.Breadcrumb = []BreadcrumbDTO{}
	}
	if err := dto.Validate(); err != nil {
		return EntryDTO{}, err
	}
	return dto, nil
}

func publicCatalogPoint(point model.RecoveryPoint) bool {
	switch backupasset.PointVersionSemantics(point.Semantics) {
	case backupasset.PointMutableHead:
		return backupasset.RecoveryPointState(point.State) == backupasset.RecoveryPointObserved
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		state := backupasset.RecoveryPointState(point.State)
		return state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
	default:
		return false
	}
}

func normalizeCatalogPageLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: Catalog page limit", ErrInvalidCatalogContract)
	}
	if limit == 0 {
		limit = defaultCatalogPageLimit
	}
	if limit > maxCatalogPageLimit {
		limit = maxCatalogPageLimit
	}
	return limit, nil
}

func (service *Service) validateRequestScope(repositoryID string, scope AuthorizationScope) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if service == nil || service.db == nil || service.ownership == nil || service.cursor == nil ||
		backupasset.ValidateOpaqueID(repositoryID) != nil {
		return fmt.Errorf("%w: invalid Catalog repository request", backupasset.ErrNotFound)
	}
	return ValidateAuthorizationScope(scope)
}

func (service *Service) validatePointScope(pointID string, scope AuthorizationScope) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if service == nil || service.db == nil || service.ownership == nil || service.cursor == nil ||
		backupasset.ValidateOpaqueID(pointID) != nil {
		return fmt.Errorf("%w: invalid Catalog point request", backupasset.ErrNotFound)
	}
	return ValidateAuthorizationScope(scope)
}

func (service *Service) ensureFeatureEnabled() error {
	if service == nil {
		return fmt.Errorf("%w: Catalog service unavailable", backupasset.ErrInvalidState)
	}
	if service.featureEnabled == nil {
		return nil
	}
	enabled, err := service.featureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return ErrFeatureDisabled
	}
	return nil
}

func (service *Service) utcNow() time.Time { return service.now().UTC() }

func sameOptionalString(value *string, expected string) bool {
	if expected == "" {
		return value == nil
	}
	return value != nil && *value == expected
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
