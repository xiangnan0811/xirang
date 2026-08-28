package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

const (
	backupSetIdentityDomain = "xirang.catalog.backup-set.v1"
	fileSourceNodeSort      = "node_id_asc"
	fileSourceBackupSetSort = "backup_set_id_asc"
	fileSourceVersionSort   = "retained_desc"
)

var errFileSourceLineageUnproven = errors.New("file-source lineage unproven")

type FileSourceLineageKind string

const (
	FileSourceLineageTask     FileSourceLineageKind = "task"
	FileSourceLineageImported FileSourceLineageKind = "imported"
)

type FileSourceBrowseState string

const (
	FileSourceBrowseStateBrowsable   FileSourceBrowseState = "browsable"
	FileSourceBrowseStateIndexing    FileSourceBrowseState = "indexing"
	FileSourceBrowseStateUnavailable FileSourceBrowseState = "unavailable"
)

type FileSourcePageRequest struct {
	Limit  int
	Cursor string
}

type FileSourceNodeDTO struct {
	NodeID               uint                          `json:"node_id"`
	DisplayName          string                        `json:"display_name"`
	BackupSetCount       int                           `json:"backup_set_count"`
	RetainedVersionCount int                           `json:"retained_version_count"`
	LatestRetainedAt     *time.Time                    `json:"latest_retained_at"`
	CatalogCoverage      CoverageStatus                `json:"catalog_coverage"`
	BrowseState          FileSourceBrowseState         `json:"browse_state" enums:"browsable,indexing,unavailable"`
	UnavailableReason    *backupasset.CapabilityReason `json:"unavailable_reason,omitempty"`
}

type FileSourceNodePage struct {
	Items      []FileSourceNodeDTO `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

type FileSourceBackupSetDTO struct {
	BackupSetID       string                        `json:"backup_set_id"`
	NodeID            uint                          `json:"node_id"`
	DisplayLabel      string                        `json:"display_label"`
	LineageKind       FileSourceLineageKind         `json:"lineage_kind" enums:"task,imported"`
	VersionCount      int                           `json:"version_count"`
	LatestRetainedAt  *time.Time                    `json:"latest_retained_at"`
	CatalogCoverage   CoverageStatus                `json:"catalog_coverage"`
	BrowseState       FileSourceBrowseState         `json:"browse_state" enums:"browsable,indexing,unavailable"`
	UnavailableReason *backupasset.CapabilityReason `json:"unavailable_reason,omitempty"`
}

type FileSourceBackupSetPage struct {
	Items      []FileSourceBackupSetDTO `json:"items"`
	NextCursor string                   `json:"next_cursor,omitempty"`
}

type FileSourceVersionDTO struct {
	RecoveryPointID     string                         `json:"recovery_point_id"`
	RepositoryID        string                         `json:"repository_id"`
	ProducingTaskID     *uint                          `json:"producing_task_id,omitempty"`
	CapturedAt          *time.Time                     `json:"captured_at"`
	CommittedAt         *time.Time                     `json:"committed_at"`
	CreatedAt           time.Time                      `json:"created_at"`
	LifecycleState      backupasset.RecoveryPointState `json:"lifecycle_state"`
	CatalogCoverage     CoverageStatus                 `json:"catalog_coverage"`
	ContentAvailability ContentAvailabilityDTO         `json:"content_availability"`
	EntryCount          int64                          `json:"entry_count"`
	LogicalBytes        int64                          `json:"logical_bytes"`
	Permissions         PermissionsDTO                 `json:"permissions"`
	BrowseState         FileSourceBrowseState          `json:"browse_state" enums:"browsable,indexing,unavailable"`
	UnavailableReason   *backupasset.CapabilityReason  `json:"unavailable_reason,omitempty"`
}

type FileSourceVersionPage struct {
	Items      []FileSourceVersionDTO `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

// FileSourceRecoveryPointDTO is the smallest authorized reverse projection
// needed to upgrade an exact legacy recovery-point deep link into the modern
// node / Backup Set hierarchy. It intentionally contains no Provider or
// content-delivery facts.
type FileSourceRecoveryPointDTO struct {
	NodeID            uint                          `json:"node_id"`
	BackupSetID       string                        `json:"backup_set_id"`
	RecoveryPointID   string                        `json:"recovery_point_id"`
	RepositoryID      string                        `json:"repository_id"`
	ProducingTaskID   *uint                         `json:"producing_task_id,omitempty"`
	BrowseState       FileSourceBrowseState         `json:"browse_state" enums:"browsable,indexing,unavailable"`
	UnavailableReason *backupasset.CapabilityReason `json:"unavailable_reason,omitempty"`
}

type fileSourcePoint struct {
	point             model.RecoveryPoint
	repository        model.BackupRepository
	setID             string
	setLabel          string
	coverage          CoverageStatus
	lineage           fileSourceLineageControl
	browseState       FileSourceBrowseState
	unavailableReason *backupasset.CapabilityReason
}

type fileSourceSetKey struct {
	kind         FileSourceLineageKind
	nodeID       uint
	taskID       uint
	repositoryID string
}

type fileSourceTaskControl struct {
	ID   uint
	Name string
}

type fileSourceNodeControl struct {
	ID   uint
	Name string
}

type fileSourceLineageControl struct {
	kind         FileSourceLineageKind
	nodeID       uint
	taskID       *uint
	repositoryID string
	label        string
}

type fileSourceLinkControl struct {
	ID               string
	TaskID           *uint
	RepositoryID     string
	TaskNameSnapshot string
	NodeIDSnapshot   uint
	NodeNameSnapshot string
}

type fileSourceProjection struct {
	nodes          []FileSourceNodeDTO
	sets           map[uint][]FileSourceBackupSetDTO
	versions       map[string][]FileSourceVersionDTO
	recoveryPoints map[string]FileSourceRecoveryPointDTO
}

func (service *Service) ResolveFileSourceRecoveryPoint(
	ctx context.Context,
	recoveryPointID string,
	scope AuthorizationScope,
) (FileSourceRecoveryPointDTO, error) {
	if backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return FileSourceRecoveryPointDTO{}, fmt.Errorf("%w: file-source recovery point", backupasset.ErrNotFound)
	}
	projection, _, err := service.fileSourceProjection(ctx, scope, FileSourcePageRequest{})
	if err != nil {
		return FileSourceRecoveryPointDTO{}, err
	}
	resolved, exists := projection.recoveryPoints[recoveryPointID]
	if !exists {
		return FileSourceRecoveryPointDTO{}, fmt.Errorf("%w: file-source recovery point", backupasset.ErrNotFound)
	}
	if resolved.RecoveryPointID != recoveryPointID {
		return FileSourceRecoveryPointDTO{}, fmt.Errorf("%w: file-source recovery point projection", ErrIdentityCollision)
	}
	return resolved, nil
}

func (service *Service) ListFileSourceNodes(
	ctx context.Context,
	scope AuthorizationScope,
	request FileSourcePageRequest,
) (FileSourceNodePage, error) {
	projection, limit, err := service.fileSourceProjection(ctx, scope, FileSourcePageRequest{Limit: request.Limit})
	if err != nil {
		return FileSourceNodePage{}, err
	}
	items := projection.nodes
	projectionDigest, err := fileSourceProjectionDigest("nodes", items)
	if err != nil {
		return FileSourceNodePage{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := 0
	cursorScope := CursorScope{
		Endpoint: CursorEndpointFileSourceNodes, Direction: CursorForward,
		UserID: scope.UserID, Role: scope.Role, Sort: fileSourceNodeSort,
		ProjectionDigest: projectionDigest,
	}
	if request.Cursor != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return FileSourceNodePage{}, decodeErr
		}
		start = -1
		for index := range items {
			if items[index].NodeID == decoded.Anchor.NodeID {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return FileSourceNodePage{}, fmt.Errorf("%w: file-source node anchor", ErrStaleCursor)
		}
	}
	end := min(start+limit, len(items))
	page := FileSourceNodePage{Items: items[start:end]}
	if end < len(items) {
		cursorScope.Anchor.NodeID = items[end-1].NodeID
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return FileSourceNodePage{}, err
		}
	}
	return page, nil
}

func (service *Service) ListFileSourceBackupSets(
	ctx context.Context,
	nodeID uint,
	scope AuthorizationScope,
	request FileSourcePageRequest,
) (FileSourceBackupSetPage, error) {
	if nodeID == 0 {
		return FileSourceBackupSetPage{}, fmt.Errorf("%w: file-source node", backupasset.ErrNotFound)
	}
	projection, limit, err := service.fileSourceProjection(ctx, scope, FileSourcePageRequest{Limit: request.Limit})
	if err != nil {
		return FileSourceBackupSetPage{}, err
	}
	items, exists := projection.sets[nodeID]
	if !exists {
		return FileSourceBackupSetPage{}, fmt.Errorf("%w: file-source node", backupasset.ErrNotFound)
	}
	projectionDigest, err := fileSourceProjectionDigest("sets", items)
	if err != nil {
		return FileSourceBackupSetPage{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := 0
	cursorScope := CursorScope{
		Endpoint: CursorEndpointFileSourceSets, Direction: CursorForward,
		UserID: scope.UserID, Role: scope.Role, Sort: fileSourceBackupSetSort, NodeID: nodeID,
		ProjectionDigest: projectionDigest,
	}
	if request.Cursor != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return FileSourceBackupSetPage{}, decodeErr
		}
		anchor := decoded.Anchor.BackupSetID
		start = -1
		for index := range items {
			if items[index].BackupSetID == anchor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return FileSourceBackupSetPage{}, fmt.Errorf("%w: file-source Backup Set anchor", ErrStaleCursor)
		}
	}
	end := min(start+limit, len(items))
	page := FileSourceBackupSetPage{Items: items[start:end]}
	if end < len(items) {
		cursorScope.Anchor.BackupSetID = items[end-1].BackupSetID
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return FileSourceBackupSetPage{}, err
		}
	}
	return page, nil
}

func (service *Service) ListFileSourceVersions(
	ctx context.Context,
	backupSetID string,
	scope AuthorizationScope,
	request FileSourcePageRequest,
) (FileSourceVersionPage, error) {
	if backupasset.ValidateOpaqueID(backupSetID) != nil {
		return FileSourceVersionPage{}, fmt.Errorf("%w: file-source Backup Set", backupasset.ErrNotFound)
	}
	projection, limit, err := service.fileSourceProjection(ctx, scope, FileSourcePageRequest{Limit: request.Limit})
	if err != nil {
		return FileSourceVersionPage{}, err
	}
	items, exists := projection.versions[backupSetID]
	if !exists {
		return FileSourceVersionPage{}, fmt.Errorf("%w: file-source Backup Set", backupasset.ErrNotFound)
	}
	projectionDigest, err := fileSourceProjectionDigest("versions", items)
	if err != nil {
		return FileSourceVersionPage{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start := 0
	cursorScope := CursorScope{
		Endpoint: CursorEndpointFileSourceVersions, Direction: CursorForward,
		UserID: scope.UserID, Role: scope.Role, Sort: fileSourceVersionSort, BackupSetID: backupSetID,
		ProjectionDigest: projectionDigest,
	}
	if request.Cursor != "" {
		decoded, decodeErr := service.cursor.Decode(ctx, request.Cursor, cursorScope)
		if decodeErr != nil {
			return FileSourceVersionPage{}, decodeErr
		}
		start = -1
		for index := range items {
			if items[index].RecoveryPointID == decoded.Anchor.RecoveryPointID {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return FileSourceVersionPage{}, fmt.Errorf("%w: file-source version anchor", ErrStaleCursor)
		}
	}
	end := min(start+limit, len(items))
	page := FileSourceVersionPage{Items: items[start:end]}
	if end < len(items) {
		cursorScope.Anchor.RecoveryPointID = items[end-1].RecoveryPointID
		page.NextCursor, err = service.cursor.Encode(ctx, cursorScope)
		if err != nil {
			return FileSourceVersionPage{}, err
		}
	}
	return page, nil
}

func (service *Service) fileSourceProjection(
	ctx context.Context,
	scope AuthorizationScope,
	request FileSourcePageRequest,
) (fileSourceProjection, int, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return fileSourceProjection{}, 0, err
	}
	if service == nil || service.db == nil || service.ownership == nil || service.cursor == nil || service.identityKeys == nil {
		return fileSourceProjection{}, 0, fmt.Errorf("%w: file-source projection unavailable", backupasset.ErrInvalidState)
	}
	if err := ValidateAuthorizationScope(scope); err != nil {
		return fileSourceProjection{}, 0, err
	}
	limit, err := normalizeCatalogPageLimit(request.Limit)
	if err != nil {
		return fileSourceProjection{}, 0, err
	}
	if request.Cursor != "" {
		return fileSourceProjection{}, 0, fmt.Errorf("%w: file-source cursor", ErrInvalidCursor)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	points, err := service.loadFileSourcePoints(ctx, scope)
	if err != nil {
		return fileSourceProjection{}, 0, err
	}
	projection, err := service.composeFileSourceProjection(ctx, points)
	if err != nil {
		return fileSourceProjection{}, 0, err
	}
	return projection, limit, nil
}

func (service *Service) loadFileSourcePoints(ctx context.Context, scope AuthorizationScope) ([]model.RecoveryPoint, error) {
	var candidateIDs []string
	query := service.db.WithContext(ctx).Table("recovery_points AS points").Select("points.id").
		Where(`((points.semantics = ? AND points.state = ?) OR
				(points.semantics IN ? AND points.state IN ?) OR
				(points.semantics = ? AND points.state IN ?))`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest)},
			[]string{string(backupasset.RecoveryPointVerifying), string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)},
			backupasset.PointImportedBaseline,
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Order("points.id ASC").Limit(maxOwnershipCandidateIDs + 1)
	if err := query.Scan(&candidateIDs).Error; err != nil {
		return nil, fmt.Errorf("load file-source candidates: %w", err)
	}
	if len(candidateIDs) > maxOwnershipCandidateIDs {
		return nil, fmt.Errorf("%w: file-source candidates", ErrOwnershipProjectionLimit)
	}
	authorized, err := service.ownership.AuthorizedPointIDs(ctx, scope, candidateIDs)
	if err != nil {
		return nil, err
	}
	if len(authorized) == 0 {
		return []model.RecoveryPoint{}, nil
	}
	var points []model.RecoveryPoint
	if err := service.db.WithContext(ctx).Select(
		"id", "repository_id", "producing_task_id", "producing_task_run_id", "producing_task_name_snapshot",
		"producing_node_id_snapshot", "producing_node_name_snapshot", "semantics", "state", "captured_at",
		"committed_at", "entry_count", "logical_bytes", "physical_availability", "created_at", "lineage_json",
		"consistency_json", "source_fingerprint", "capability_revision",
	).Where("id IN ?", authorized).Order("id ASC").Find(&points).Error; err != nil {
		return nil, fmt.Errorf("load authorized file-source points: %w", err)
	}
	if len(points) != len(authorized) {
		return nil, fmt.Errorf("%w: file-source point changed", ErrCatalogUnavailable)
	}
	return points, nil
}

func (service *Service) composeFileSourceProjection(ctx context.Context, points []model.RecoveryPoint) (fileSourceProjection, error) {
	projection := fileSourceProjection{
		nodes: []FileSourceNodeDTO{}, sets: make(map[uint][]FileSourceBackupSetDTO),
		versions: make(map[string][]FileSourceVersionDTO), recoveryPoints: make(map[string]FileSourceRecoveryPointDTO),
	}
	if len(points) == 0 {
		return projection, nil
	}
	links, err := service.loadFileSourceLinks(ctx, points)
	if err != nil {
		return fileSourceProjection{}, err
	}
	repositories, err := service.loadFileSourceRepositories(ctx, points)
	if err != nil {
		return fileSourceProjection{}, err
	}
	coverage, err := service.loadFileSourceCoverage(ctx, points)
	if err != nil {
		return fileSourceProjection{}, err
	}
	type validatedPoint struct {
		point      model.RecoveryPoint
		repository model.BackupRepository
		lineage    fileSourceLineageControl
	}
	validated := make([]validatedPoint, 0, len(points))
	validatedModels := make([]model.RecoveryPoint, 0, len(points))
	nodeIDs := make(map[uint]struct{})
	nodeLabels := make(map[uint]string)
	for _, point := range points {
		repository, exists := repositories[point.RepositoryID]
		if !exists {
			return fileSourceProjection{}, fmt.Errorf("%w: file-source repository changed", ErrCatalogUnavailable)
		}
		lineage, validateErr := validateFileSourcePoint(point, repository, links)
		if errors.Is(validateErr, errFileSourceLineageUnproven) {
			continue
		}
		if validateErr != nil {
			return fileSourceProjection{}, validateErr
		}
		point.ProducingTaskID = lineage.taskID
		validated = append(validated, validatedPoint{point: point, repository: repository, lineage: lineage})
		validatedModels = append(validatedModels, point)
		nodeIDs[lineage.nodeID] = struct{}{}
		label, labelErr := sanitizeFileSourceLabel(point.ProducingNodeNameSnapshot)
		if labelErr != nil {
			return fileSourceProjection{}, labelErr
		}
		if _, exists := nodeLabels[lineage.nodeID]; !exists {
			nodeLabels[lineage.nodeID] = label
		}
	}
	if len(validated) == 0 {
		return projection, nil
	}
	tasks, err := service.loadFileSourceTasks(ctx, validatedModels)
	if err != nil {
		return fileSourceProjection{}, err
	}
	nodes, err := service.loadFileSourceNodes(ctx, nodeIDs)
	if err != nil {
		return fileSourceProjection{}, err
	}
	identityKey, err := service.fileSourceIdentityKey(ctx)
	if err != nil {
		return fileSourceProjection{}, err
	}

	sets := make(map[fileSourceSetKey][]fileSourcePoint)
	setCoordinates := make(map[string]fileSourceSetKey)
	for _, retained := range validated {
		point, repository, lineage := retained.point, retained.repository, retained.lineage
		nodeID := lineage.nodeID
		if lineage.taskID != nil {
			if task, exists := tasks[*lineage.taskID]; exists {
				label, labelErr := sanitizeFileSourceLabel(task.Name)
				if labelErr != nil {
					return fileSourceProjection{}, labelErr
				}
				lineage.label = label
			}
		}
		if node, nodeExists := nodes[nodeID]; nodeExists {
			label, labelErr := sanitizeFileSourceLabel(node.Name)
			if labelErr != nil {
				return fileSourceProjection{}, labelErr
			}
			nodeLabels[nodeID] = label
		}
		key, label, err := fileSourceSetCoordinates(lineage)
		if err != nil {
			return fileSourceProjection{}, err
		}
		setID := deriveBackupSetID(identityKey, key)
		if existing, collision := setCoordinates[setID]; collision && existing != key {
			return fileSourceProjection{}, fmt.Errorf("%w: Backup Set identity", ErrIdentityCollision)
		}
		setCoordinates[setID] = key
		browseState, unavailableReason, err := fileSourcePointBrowseState(point, repository, coverage[point.ID])
		if err != nil {
			return fileSourceProjection{}, err
		}
		sets[key] = append(sets[key], fileSourcePoint{point: point, repository: repository, setID: setID, setLabel: label,
			coverage: coverage[point.ID], lineage: lineage, browseState: browseState, unavailableReason: unavailableReason})
	}

	nodeSets := make(map[uint][]FileSourceBackupSetDTO)
	nodeVersions := make(map[uint][]FileSourceVersionDTO)
	for key, grouped := range sets {
		sort.Slice(grouped, func(left, right int) bool {
			return fileSourceVersionLess(grouped[left].point, grouped[right].point)
		})
		versions := make([]FileSourceVersionDTO, 0, len(grouped))
		coverages := make([]CoverageStatus, 0, len(grouped))
		browseStates := make([]FileSourceBrowseState, 0, len(grouped))
		unavailableReasons := make([]*backupasset.CapabilityReason, 0, len(grouped))
		for _, item := range grouped {
			version := FileSourceVersionDTO{
				RecoveryPointID: item.point.ID, RepositoryID: item.point.RepositoryID,
				ProducingTaskID: item.lineage.taskID, CapturedAt: utcPointer(item.point.CapturedAt),
				CommittedAt: utcPointer(item.point.CommittedAt), CreatedAt: item.point.CreatedAt.UTC(),
				LifecycleState: backupasset.RecoveryPointState(item.point.State), CatalogCoverage: item.coverage,
				ContentAvailability: fileSourceContentAvailability(item.point, item.repository), EntryCount: item.point.EntryCount,
				LogicalBytes: item.point.LogicalBytes, Permissions: PermissionsDTO{List: true},
				BrowseState: item.browseState, UnavailableReason: item.unavailableReason,
			}
			if err := version.validate(); err != nil {
				return fileSourceProjection{}, err
			}
			resolved := FileSourceRecoveryPointDTO{
				NodeID: key.nodeID, BackupSetID: item.setID, RecoveryPointID: version.RecoveryPointID,
				RepositoryID: version.RepositoryID, ProducingTaskID: version.ProducingTaskID,
				BrowseState: version.BrowseState, UnavailableReason: version.UnavailableReason,
			}
			if _, duplicate := projection.recoveryPoints[resolved.RecoveryPointID]; duplicate {
				return fileSourceProjection{}, fmt.Errorf("%w: duplicate file-source recovery point", ErrIdentityCollision)
			}
			if err := resolved.validate(); err != nil {
				return fileSourceProjection{}, err
			}
			projection.recoveryPoints[resolved.RecoveryPointID] = resolved
			versions = append(versions, version)
			coverages = append(coverages, item.coverage)
			browseStates = append(browseStates, item.browseState)
			unavailableReasons = append(unavailableReasons, item.unavailableReason)
		}
		setBrowseState, setUnavailableReason := aggregateFileSourceBrowseState(browseStates, unavailableReasons)
		first := grouped[0]
		set := FileSourceBackupSetDTO{
			BackupSetID: first.setID, NodeID: key.nodeID, DisplayLabel: first.setLabel,
			LineageKind: key.kind, VersionCount: len(versions),
			LatestRetainedAt: fileSourceRetainedAt(grouped[0].point), CatalogCoverage: aggregateFileSourceCoverage(coverages),
			BrowseState: setBrowseState, UnavailableReason: setUnavailableReason,
		}
		if err := set.validate(); err != nil {
			return fileSourceProjection{}, err
		}
		nodeSets[key.nodeID] = append(nodeSets[key.nodeID], set)
		projection.versions[first.setID] = versions
		nodeVersions[key.nodeID] = append(nodeVersions[key.nodeID], versions...)
	}

	for nodeID, items := range nodeSets {
		sort.Slice(items, func(left, right int) bool { return items[left].BackupSetID < items[right].BackupSetID })
		projection.sets[nodeID] = items
		versions := nodeVersions[nodeID]
		sort.Slice(versions, func(left, right int) bool { return fileSourceVersionDTOLess(versions[left], versions[right]) })
		coverageValues := make([]CoverageStatus, 0, len(versions))
		browseStates := make([]FileSourceBrowseState, 0, len(versions))
		unavailableReasons := make([]*backupasset.CapabilityReason, 0, len(versions))
		for _, version := range versions {
			coverageValues = append(coverageValues, version.CatalogCoverage)
			browseStates = append(browseStates, version.BrowseState)
			unavailableReasons = append(unavailableReasons, version.UnavailableReason)
		}
		nodeBrowseState, nodeUnavailableReason := aggregateFileSourceBrowseState(browseStates, unavailableReasons)
		dto := FileSourceNodeDTO{
			NodeID: nodeID, DisplayName: nodeLabels[nodeID], BackupSetCount: len(items), RetainedVersionCount: len(versions),
			LatestRetainedAt: fileSourceVersionRetainedAt(versions[0]), CatalogCoverage: aggregateFileSourceCoverage(coverageValues),
			BrowseState: nodeBrowseState, UnavailableReason: nodeUnavailableReason,
		}
		if err := dto.validate(); err != nil {
			return fileSourceProjection{}, err
		}
		projection.nodes = append(projection.nodes, dto)
	}
	sort.Slice(projection.nodes, func(left, right int) bool { return projection.nodes[left].NodeID < projection.nodes[right].NodeID })
	return projection, nil
}

func (service *Service) loadFileSourceTasks(ctx context.Context, points []model.RecoveryPoint) (map[uint]fileSourceTaskControl, error) {
	ids := make([]uint, 0)
	seen := make(map[uint]struct{})
	for _, point := range points {
		if point.ProducingTaskID == nil || backupasset.PointVersionSemantics(point.Semantics) == backupasset.PointImportedBaseline {
			continue
		}
		if _, exists := seen[*point.ProducingTaskID]; !exists {
			seen[*point.ProducingTaskID] = struct{}{}
			ids = append(ids, *point.ProducingTaskID)
		}
	}
	result := make(map[uint]fileSourceTaskControl, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []fileSourceTaskControl
	if err := service.db.WithContext(ctx).Table("tasks").Select("id", "name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load file-source tasks: %w", err)
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func (service *Service) loadFileSourceNodes(ctx context.Context, ids map[uint]struct{}) (map[uint]fileSourceNodeControl, error) {
	values := make([]uint, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	var rows []fileSourceNodeControl
	if err := service.db.WithContext(ctx).Table("nodes").Select("id", "name").Where("id IN ?", values).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load file-source nodes: %w", err)
	}
	result := make(map[uint]fileSourceNodeControl, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func (service *Service) loadFileSourceLinks(ctx context.Context, points []model.RecoveryPoint) (map[string]fileSourceLinkControl, error) {
	ids := make([]string, 0, len(points))
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		semantics := backupasset.PointVersionSemantics(point.Semantics)
		if semantics != backupasset.PointNativeSnapshot && semantics != backupasset.PointXirangManifest {
			continue
		}
		lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
		if err != nil {
			continue
		}
		if _, exists := seen[lineage.TaskRepositoryLinkID]; !exists {
			seen[lineage.TaskRepositoryLinkID] = struct{}{}
			ids = append(ids, lineage.TaskRepositoryLinkID)
		}
	}
	result := make(map[string]fileSourceLinkControl, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []fileSourceLinkControl
	if err := service.db.WithContext(ctx).Table("task_repository_links").Select(
		"id", "task_id", "repository_id", "task_name_snapshot", "node_id_snapshot", "node_name_snapshot",
	).Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load file-source lineage links: %w", err)
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func (service *Service) loadFileSourceRepositories(ctx context.Context, points []model.RecoveryPoint) (map[string]model.BackupRepository, error) {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, point := range points {
		if _, exists := seen[point.RepositoryID]; !exists {
			seen[point.RepositoryID] = struct{}{}
			ids = append(ids, point.RepositoryID)
		}
	}
	var rows []model.BackupRepository
	if err := service.db.WithContext(ctx).Select(
		"id", "provider_kind", "version_mode", "status", "capability_revision", "capabilities_json", "immutability_level",
	).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load file-source repositories: %w", err)
	}
	result := make(map[string]model.BackupRepository, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	if len(result) != len(ids) {
		return nil, fmt.Errorf("%w: file-source repository changed", ErrCatalogUnavailable)
	}
	return result, nil
}

func (service *Service) loadFileSourceCoverage(ctx context.Context, points []model.RecoveryPoint) (map[string]CoverageStatus, error) {
	ids := make([]string, 0, len(points))
	for _, point := range points {
		ids = append(ids, point.ID)
	}
	var rows []model.CatalogGeneration
	if err := service.db.WithContext(ctx).Select("id", "recovery_point_id", "generation", "state", "is_active").
		Where("recovery_point_id IN ?", ids).Order("recovery_point_id ASC, generation ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load file-source Catalog coverage: %w", err)
	}
	type generationState struct {
		active *model.CatalogGeneration
		latest *model.CatalogGeneration
	}
	states := make(map[string]generationState, len(points))
	for index := range rows {
		row := rows[index]
		state, err := ParseGenerationState(row.State)
		if err != nil || backupasset.ValidateOpaqueID(row.ID) != nil || row.Generation <= 0 {
			return nil, fmt.Errorf("%w: file-source Catalog generation", ErrUnknownInternalState)
		}
		current := states[row.RecoveryPointID]
		current.latest = &rows[index]
		if row.IsActive {
			if state != GenerationComplete || current.active != nil {
				return nil, fmt.Errorf("%w: file-source active Catalog generation", ErrUnknownInternalState)
			}
			current.active = &rows[index]
		}
		states[row.RecoveryPointID] = current
	}
	result := make(map[string]CoverageStatus, len(points))
	for _, point := range points {
		state := states[point.ID]
		if state.active != nil {
			result[point.ID] = CoverageComplete
			continue
		}
		if state.latest == nil {
			result[point.ID] = CoverageUnavailable
			continue
		}
		parsed, _ := ParseGenerationState(state.latest.State)
		result[point.ID] = coverageFromGeneration(parsed)
	}
	return result, nil
}

func (service *Service) fileSourceIdentityKey(ctx context.Context) ([]byte, error) {
	material, err := service.identityKeys.Active(ctx, backupasset.KeyDomainEntryIdentity)
	if err != nil || material.Domain != backupasset.KeyDomainEntryIdentity || material.State != backupasset.DomainKeyActive || len(material.Key) < 32 {
		return nil, fmt.Errorf("%w: Backup Set identity key unavailable", ErrIdentityKeyUnavailable)
	}
	return material.Key, nil
}

func fileSourceNodeID(point model.RecoveryPoint) (uint, error) {
	if point.ProducingNodeIDSnapshot == 0 {
		return 0, fmt.Errorf("%w: file-source node lineage unavailable", ErrCatalogUnavailable)
	}
	return point.ProducingNodeIDSnapshot, nil
}

func fileSourceSetCoordinates(lineage fileSourceLineageControl) (fileSourceSetKey, string, error) {
	if lineage.kind == FileSourceLineageTask && lineage.taskID != nil && *lineage.taskID > 0 {
		return fileSourceSetKey{kind: lineage.kind, nodeID: lineage.nodeID, taskID: *lineage.taskID}, lineage.label, nil
	}
	if lineage.kind != FileSourceLineageImported || lineage.nodeID == 0 {
		return fileSourceSetKey{}, "", fmt.Errorf("%w: file-source lineage coordinates", ErrInvalidCatalogContract)
	}
	if backupasset.ValidateOpaqueID(lineage.repositoryID) != nil {
		return fileSourceSetKey{}, "", fmt.Errorf("%w: file-source repository lineage", ErrInvalidCatalogContract)
	}
	return fileSourceSetKey{kind: lineage.kind, nodeID: lineage.nodeID, repositoryID: lineage.repositoryID}, lineage.label, nil
}

func deriveBackupSetID(key []byte, coordinates fileSourceSetKey) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(backupSetIdentityDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(coordinates.kind))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatUint(uint64(coordinates.nodeID), 10)))
	_, _ = mac.Write([]byte{0})
	if coordinates.kind == FileSourceLineageTask {
		_, _ = mac.Write([]byte(strconv.FormatUint(uint64(coordinates.taskID), 10)))
	} else {
		_, _ = mac.Write([]byte(coordinates.repositoryID))
	}
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func fileSourceProjectionDigest(kind string, projection any) (string, error) {
	payload, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("%w: file-source projection digest", ErrInvalidCatalogContract)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.catalog.file-source-page.v1"))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(kind))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateFileSourcePoint(
	point model.RecoveryPoint,
	repository model.BackupRepository,
	links map[string]fileSourceLinkControl,
) (fileSourceLineageControl, error) {
	if backupasset.ValidateOpaqueID(point.ID) != nil || backupasset.ValidateOpaqueID(point.RepositoryID) != nil ||
		point.RepositoryID != repository.ID || !retainedFileSourcePoint(point) || point.CreatedAt.IsZero() ||
		(point.CapturedAt != nil && point.CapturedAt.IsZero()) || (point.CommittedAt != nil && point.CommittedAt.IsZero()) ||
		point.EntryCount < 0 || point.LogicalBytes < 0 || !validFileSourceRepositoryStatus(backupasset.RepositoryStatus(repository.Status)) ||
		!validFileSourcePhysicalAvailability(backupasset.PhysicalAvailability(point.PhysicalAvailability)) {
		return fileSourceLineageControl{}, fmt.Errorf("%w: invalid file-source point", ErrUnknownInternalState)
	}
	nodeID, err := fileSourceNodeID(point)
	if err != nil {
		return fileSourceLineageControl{}, fmt.Errorf("%w: %v", errFileSourceLineageUnproven, err)
	}
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	if semantics == backupasset.PointImportedBaseline {
		if point.ProducingTaskID != nil || point.ProducingTaskRunID != nil {
			return fileSourceLineageControl{}, fmt.Errorf("%w: imported attribution", errFileSourceLineageUnproven)
		}
		return fileSourceLineageControl{
			kind: FileSourceLineageImported, nodeID: nodeID, repositoryID: point.RepositoryID, label: "Imported backup",
		}, nil
	}
	if semantics == backupasset.PointMutableHead {
		var lineage backupasset.RecoveryPointLineageSummary
		if err := decodeStrictFileSourceJSON(point.LineageJSON, &lineage); err != nil || lineage.ProducingTaskID == nil ||
			*lineage.ProducingTaskID == 0 || lineage.ProducingTaskRunID != nil || lineage.SourcePointID != "" ||
			point.ProducingTaskID == nil || *point.ProducingTaskID != *lineage.ProducingTaskID || point.ProducingTaskRunID != nil {
			return fileSourceLineageControl{}, fmt.Errorf("%w: mutable attribution", errFileSourceLineageUnproven)
		}
		label, err := sanitizeFileSourceLabel(point.ProducingTaskNameSnapshot)
		if err != nil {
			return fileSourceLineageControl{}, err
		}
		taskID := *lineage.ProducingTaskID
		return fileSourceLineageControl{kind: FileSourceLineageTask, nodeID: nodeID, taskID: &taskID, label: label}, nil
	}

	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || (point.ProducingTaskID != nil && *point.ProducingTaskID != lineage.TaskID) ||
		(point.ProducingTaskRunID != nil && *point.ProducingTaskRunID != lineage.TaskRunID) {
		return fileSourceLineageControl{}, fmt.Errorf("%w: immutable attribution", errFileSourceLineageUnproven)
	}
	link, exists := links[lineage.TaskRepositoryLinkID]
	if !exists || link.RepositoryID != point.RepositoryID || (link.TaskID != nil && *link.TaskID != lineage.TaskID) ||
		link.NodeIDSnapshot != nodeID || strings.TrimSpace(link.TaskNameSnapshot) != strings.TrimSpace(point.ProducingTaskNameSnapshot) ||
		strings.TrimSpace(link.NodeNameSnapshot) != strings.TrimSpace(point.ProducingNodeNameSnapshot) {
		return fileSourceLineageControl{}, fmt.Errorf("%w: managed lineage mismatch", errFileSourceLineageUnproven)
	}
	if backupasset.RecoveryPointState(point.State) == backupasset.RecoveryPointVerifying {
		if !fileSourceManagedRetainedEvidence(point, repository) {
			return fileSourceLineageControl{}, fmt.Errorf("%w: retained evidence", errFileSourceLineageUnproven)
		}
	}
	label, err := sanitizeFileSourceLabel(point.ProducingTaskNameSnapshot)
	if err != nil {
		return fileSourceLineageControl{}, err
	}
	taskID := lineage.TaskID
	return fileSourceLineageControl{kind: FileSourceLineageTask, nodeID: nodeID, taskID: &taskID, label: label}, nil
}

func retainedFileSourcePoint(point model.RecoveryPoint) bool {
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	state := backupasset.RecoveryPointState(point.State)
	if semantics == backupasset.PointMutableHead {
		return state == backupasset.RecoveryPointObserved
	}
	if semantics == backupasset.PointImportedBaseline {
		return state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
	}
	return (semantics == backupasset.PointNativeSnapshot || semantics == backupasset.PointXirangManifest) &&
		(state == backupasset.RecoveryPointVerifying || state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded)
}

func decodeStrictFileSourceJSON(raw string, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func fileSourcePointBrowseState(
	point model.RecoveryPoint,
	repository model.BackupRepository,
	coverage CoverageStatus,
) (FileSourceBrowseState, *backupasset.CapabilityReason, error) {
	availability := fileSourceContentAvailability(point, repository)
	if !availability.Available {
		return FileSourceBrowseStateUnavailable, safeFileSourceReason(availability.Reason), nil
	}
	repositoryDTO, err := backupasset.ToRepositoryDTO(repository)
	if err != nil {
		return "", nil, fmt.Errorf("%w: file-source repository capabilities", ErrUnknownInternalState)
	}
	if !repositoryDTO.Capabilities.OpenSequential {
		return FileSourceBrowseStateUnavailable, &backupasset.CapabilityReason{Code: backupasset.CapabilitySequentialReadUnavailable}, nil
	}
	if publicCatalogPoint(point) && coverage == CoverageComplete {
		return FileSourceBrowseStateBrowsable, nil, nil
	}
	return FileSourceBrowseStateIndexing, nil, nil
}

// Managed immutable points begin life with physical availability unknown. Once
// exact provider-commit evidence has been persisted, an online repository is
// sufficient retained-data truth for Files: the point is indexing until its
// Catalog is complete, then browsable. Explicit offline/missing point facts and
// repository health still close availability.
func fileSourceContentAvailability(point model.RecoveryPoint, repository model.BackupRepository) ContentAvailabilityDTO {
	availability := contentAvailability(point, repository)
	if availability.Available || backupasset.RepositoryStatus(repository.Status) != backupasset.RepositoryOnline ||
		backupasset.PhysicalAvailability(point.PhysicalAvailability) != backupasset.PhysicalUnknown {
		return availability
	}
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	if semantics != backupasset.PointNativeSnapshot && semantics != backupasset.PointXirangManifest {
		return availability
	}
	if !fileSourceManagedRetainedEvidence(point, repository) {
		return availability
	}
	return ContentAvailabilityDTO{Available: true}
}

func fileSourceManagedRetainedEvidence(point model.RecoveryPoint, repository model.BackupRepository) bool {
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || consistency.Provider != backupasset.ProviderKind(repository.ProviderKind) ||
		!validFileSourceDigest(consistency.ProviderCommitDigest) || !validFileSourceDigest(consistency.RepositoryIdentityDigest) ||
		!validFileSourceDigest(point.SourceFingerprint) || consistency.CapabilityRevision != point.CapabilityRevision ||
		point.CapabilityRevision <= 0 || consistency.Code != "" || consistency.Completion != "" {
		return false
	}
	switch consistency.Provider {
	case backupasset.ProviderRestic:
		return backupasset.PointVersionSemantics(point.Semantics) == backupasset.PointNativeSnapshot &&
			validFileSourceDigest(consistency.RequestedTagDigest) && consistency.CaptureStartedAt != nil &&
			consistency.CaptureFinishedAt != nil && strings.TrimSpace(consistency.AdapterRevision) != ""
	case backupasset.ProviderRclone, backupasset.ProviderRsync:
		return backupasset.PointVersionSemantics(point.Semantics) == backupasset.PointXirangManifest &&
			consistency.RequestedTagDigest == "" && consistency.CaptureStartedAt == nil && consistency.CaptureFinishedAt == nil
	default:
		return false
	}
}

func validFileSourceDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func safeFileSourceReason(reason *backupasset.CapabilityReason) *backupasset.CapabilityReason {
	if reason == nil {
		return &backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}
	}
	return &backupasset.CapabilityReason{Code: reason.Code}
}

func aggregateFileSourceBrowseState(
	states []FileSourceBrowseState,
	reasons []*backupasset.CapabilityReason,
) (FileSourceBrowseState, *backupasset.CapabilityReason) {
	for _, state := range states {
		if state == FileSourceBrowseStateBrowsable {
			return FileSourceBrowseStateBrowsable, nil
		}
	}
	for _, state := range states {
		if state == FileSourceBrowseStateIndexing {
			return FileSourceBrowseStateIndexing, nil
		}
	}
	var reason *backupasset.CapabilityReason
	for _, candidate := range reasons {
		if candidate == nil {
			continue
		}
		if reason == nil {
			reason = safeFileSourceReason(candidate)
			continue
		}
		if reason.Code != candidate.Code {
			return FileSourceBrowseStateUnavailable, &backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}
		}
	}
	return FileSourceBrowseStateUnavailable, safeFileSourceReason(reason)
}

func validFileSourceRepositoryStatus(status backupasset.RepositoryStatus) bool {
	switch status {
	case backupasset.RepositoryConnecting, backupasset.RepositoryOnline, backupasset.RepositoryDegraded,
		backupasset.RepositoryOffline, backupasset.RepositoryDisconnected, backupasset.RepositoryPurging,
		backupasset.RepositoryPurgeBlocked:
		return true
	default:
		return false
	}
}

func validFileSourcePhysicalAvailability(value backupasset.PhysicalAvailability) bool {
	return value == backupasset.PhysicalOnline || value == backupasset.PhysicalOffline ||
		value == backupasset.PhysicalMissing || value == backupasset.PhysicalUnknown
}

func sanitizeFileSourceLabel(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 255 || strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", fmt.Errorf("%w: unsafe file-source display label", ErrInvalidCatalogContract)
	}
	return trimmed, nil
}

func (item FileSourceNodeDTO) validate() error {
	if item.NodeID == 0 || item.BackupSetCount <= 0 || item.RetainedVersionCount < item.BackupSetCount ||
		!validCoverageStatuses[item.CatalogCoverage] || !validFileSourceBrowseState(item.BrowseState) ||
		(item.LatestRetainedAt != nil && !isUTC(*item.LatestRetainedAt)) {
		return fmt.Errorf("%w: invalid file-source node", ErrInvalidCatalogContract)
	}
	if err := validateFileSourceUnavailableReason(item.BrowseState, item.UnavailableReason); err != nil {
		return err
	}
	_, err := sanitizeFileSourceLabel(item.DisplayName)
	return err
}

func (item FileSourceBackupSetDTO) validate() error {
	if backupasset.ValidateOpaqueID(item.BackupSetID) != nil || item.NodeID == 0 || item.VersionCount <= 0 ||
		(item.LineageKind != FileSourceLineageTask && item.LineageKind != FileSourceLineageImported) ||
		!validCoverageStatuses[item.CatalogCoverage] || !validFileSourceBrowseState(item.BrowseState) ||
		(item.LatestRetainedAt != nil && !isUTC(*item.LatestRetainedAt)) {
		return fmt.Errorf("%w: invalid file-source Backup Set", ErrInvalidCatalogContract)
	}
	if err := validateFileSourceUnavailableReason(item.BrowseState, item.UnavailableReason); err != nil {
		return err
	}
	_, err := sanitizeFileSourceLabel(item.DisplayLabel)
	return err
}

func (item FileSourceVersionDTO) validate() error {
	if backupasset.ValidateOpaqueID(item.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(item.RepositoryID) != nil ||
		item.CreatedAt.IsZero() || !isUTC(item.CreatedAt) || item.EntryCount < 0 || item.LogicalBytes < 0 ||
		(item.CapturedAt != nil && !isUTC(*item.CapturedAt)) || (item.CommittedAt != nil && !isUTC(*item.CommittedAt)) ||
		!validCoverageStatuses[item.CatalogCoverage] || !validFileSourceLifecycleState(item.LifecycleState) ||
		!item.Permissions.List || item.Permissions.Preview || item.Permissions.Download ||
		!validFileSourceBrowseState(item.BrowseState) ||
		(item.ContentAvailability.Available && item.ContentAvailability.Reason != nil) ||
		(!item.ContentAvailability.Available && item.ContentAvailability.Reason == nil) ||
		(item.BrowseState != FileSourceBrowseStateUnavailable && !item.ContentAvailability.Available) {
		return fmt.Errorf("%w: invalid file-source version", ErrInvalidCatalogContract)
	}
	if item.ContentAvailability.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*item.ContentAvailability.Reason); err != nil {
			return fmt.Errorf("%w: invalid file-source content availability", ErrInvalidCatalogContract)
		}
	}
	if err := validateFileSourceUnavailableReason(item.BrowseState, item.UnavailableReason); err != nil {
		return err
	}
	return nil
}

func (item FileSourceRecoveryPointDTO) validate() error {
	if item.NodeID == 0 || backupasset.ValidateOpaqueID(item.BackupSetID) != nil ||
		backupasset.ValidateOpaqueID(item.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(item.RepositoryID) != nil ||
		(item.ProducingTaskID != nil && *item.ProducingTaskID == 0) || !validFileSourceBrowseState(item.BrowseState) {
		return fmt.Errorf("%w: invalid file-source recovery point", ErrInvalidCatalogContract)
	}
	return validateFileSourceUnavailableReason(item.BrowseState, item.UnavailableReason)
}

func validFileSourceLifecycleState(state backupasset.RecoveryPointState) bool {
	return state == backupasset.RecoveryPointObserved || state == backupasset.RecoveryPointVerifying ||
		state == backupasset.RecoveryPointCommitted || state == backupasset.RecoveryPointDegraded
}

func validFileSourceBrowseState(state FileSourceBrowseState) bool {
	return state == FileSourceBrowseStateBrowsable || state == FileSourceBrowseStateIndexing || state == FileSourceBrowseStateUnavailable
}

func validateFileSourceUnavailableReason(state FileSourceBrowseState, reason *backupasset.CapabilityReason) error {
	if state != FileSourceBrowseStateUnavailable {
		if reason != nil {
			return fmt.Errorf("%w: unexpected file-source unavailable reason", ErrInvalidCatalogContract)
		}
		return nil
	}
	if reason == nil || len(reason.Params) != 0 {
		return fmt.Errorf("%w: missing file-source unavailable reason", ErrInvalidCatalogContract)
	}
	if err := backupasset.ValidateCapabilityReason(*reason); err != nil {
		return fmt.Errorf("%w: invalid file-source unavailable reason", ErrInvalidCatalogContract)
	}
	return nil
}

func aggregateFileSourceCoverage(values []CoverageStatus) CoverageStatus {
	if len(values) == 0 {
		return CoverageUnavailable
	}
	first := values[0]
	allSame := true
	for _, value := range values {
		if value != first {
			allSame = false
			break
		}
	}
	if allSame {
		return first
	}
	return CoveragePartial
}

func fileSourceVersionLess(left, right model.RecoveryPoint) bool {
	if comparison := compareOptionalTimeDesc(left.CapturedAt, right.CapturedAt); comparison != 0 {
		return comparison < 0
	}
	if comparison := compareOptionalTimeDesc(left.CommittedAt, right.CommittedAt); comparison != 0 {
		return comparison < 0
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID > right.ID
}

func fileSourceVersionDTOLess(left, right FileSourceVersionDTO) bool {
	if comparison := compareOptionalTimeDesc(left.CapturedAt, right.CapturedAt); comparison != 0 {
		return comparison < 0
	}
	if comparison := compareOptionalTimeDesc(left.CommittedAt, right.CommittedAt); comparison != 0 {
		return comparison < 0
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.RecoveryPointID > right.RecoveryPointID
}

func compareOptionalTimeDesc(left, right *time.Time) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if left.Equal(*right) {
		return 0
	}
	if left.After(*right) {
		return -1
	}
	return 1
}

func fileSourceRetainedAt(point model.RecoveryPoint) *time.Time {
	if point.CapturedAt != nil {
		return utcPointer(point.CapturedAt)
	}
	if point.CommittedAt != nil {
		return utcPointer(point.CommittedAt)
	}
	value := point.CreatedAt.UTC()
	return &value
}

func fileSourceVersionRetainedAt(version FileSourceVersionDTO) *time.Time {
	if version.CapturedAt != nil {
		return utcPointer(version.CapturedAt)
	}
	if version.CommittedAt != nil {
		return utcPointer(version.CommittedAt)
	}
	value := version.CreatedAt.UTC()
	return &value
}
