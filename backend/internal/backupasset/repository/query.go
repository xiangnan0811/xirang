package repository

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultRepositoryPageLimit  = 100
	maxRepositoryPageLimit      = 200
	lineageSourceTaskLink       = "task_link"
	lineageSourceRecoveryPoint  = "recovery_point"
	repositoryCursorVersion     = 1
	repositoryCursorTTL         = 15 * time.Minute
	repositoryCursorMaxBytes    = 4096
	repositoryCursorDomain      = "backup-repository-list-cursor:v1"
	managedRsyncReaderPageSize  = 200
	managedRsyncReaderCursorTTL = 15 * time.Minute
	rsyncRestoreImmutableSource = "immutable"
)

type VisibilityScope struct {
	Role   string
	UserID uint
}

type RepositoryListRequest struct {
	Limit  int
	Cursor string
}

type RepositoryPage struct {
	Items      []RepositoryView `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type RepositoryView struct {
	backupasset.RepositoryDTO
	AccessActive bool                         `json:"access_active"`
	Lineages     []LineageSummary             `json:"lineages"`
	Catalog      catalog.RepositorySummaryDTO `json:"catalog"`
}

type LineageSummary struct {
	Source               string `json:"source"`
	TaskRepositoryLinkID string `json:"task_repository_link_id,omitempty"`
	TaskID               *uint  `json:"task_id,omitempty"`
	TaskName             string `json:"task_name"`
	NodeID               uint   `json:"node_id"`
	NodeName             string `json:"node_name"`
	PublicationMode      string `json:"publication_mode,omitempty"`
	RecoveryPointID      string `json:"recovery_point_id,omitempty"`
	RecoveryPointState   string `json:"recovery_point_state,omitempty"`
	PointSemantics       string `json:"point_semantics,omitempty"`
	Active               bool   `json:"active"`
}

func (service *Service) List(ctx context.Context, request RepositoryListRequest, scope VisibilityScope, requestContext RequestContext) (RepositoryPage, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return RepositoryPage{}, err
	}
	if service.db == nil {
		return RepositoryPage{}, fmt.Errorf("%w: repository database unavailable", backupasset.ErrInvalidState)
	}
	if err := validateVisibilityScope(scope); err != nil {
		return RepositoryPage{}, err
	}
	limit, err := normalizeRepositoryPageLimit(request.Limit)
	if err != nil {
		return RepositoryPage{}, err
	}
	var cursor *repositoryCursor
	if strings.TrimSpace(request.Cursor) != "" {
		decoded, err := service.decodeRepositoryCursor(ctx, request.Cursor, scope)
		if err != nil {
			return RepositoryPage{}, err
		}
		cursor = &decoded
	}
	var repositories []model.BackupRepository
	var hasMore bool
	if scope.Role == "operator" {
		repositories, hasMore, err = service.listOperatorRepositories(ctx, limit, cursor, scope)
		if err != nil {
			return RepositoryPage{}, err
		}
	} else {
		query := service.db.WithContext(ctx).Model(&model.BackupRepository{})
		if cursor != nil {
			query = query.Where(`backup_repositories.created_at < ? OR
				(backup_repositories.created_at = ? AND backup_repositories.id < ?)`, cursor.CreatedAt, cursor.CreatedAt, cursor.RepositoryID)
		}
		if err := query.Order("backup_repositories.created_at DESC, backup_repositories.id DESC").Limit(limit + 1).Find(&repositories).Error; err != nil {
			return RepositoryPage{}, fmt.Errorf("list visible backup repositories: %w", err)
		}
		hasMore = len(repositories) > limit
		if hasMore {
			repositories = repositories[:limit]
		}
	}
	page := RepositoryPage{Items: make([]RepositoryView, 0, len(repositories))}
	for _, repository := range repositories {
		view, err := service.repositoryView(ctx, repository, scope)
		if err != nil {
			return RepositoryPage{}, err
		}
		page.Items = append(page.Items, view)
	}
	if hasMore && len(repositories) > 0 {
		last := repositories[len(repositories)-1]
		page.NextCursor, err = service.encodeRepositoryCursor(ctx, scope, last.CreatedAt, last.ID)
		if err != nil {
			return RepositoryPage{}, err
		}
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryList, backupasset.AuditOutcomeSuccess, "", nil, "list", nil)
	return page, nil
}

func (service *Service) Detail(ctx context.Context, repositoryID string, scope VisibilityScope, requestContext RequestContext) (RepositoryView, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return RepositoryView{}, err
	}
	if service.db == nil {
		return RepositoryView{}, fmt.Errorf("%w: repository database unavailable", backupasset.ErrInvalidState)
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil {
		return RepositoryView{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	if err := validateVisibilityScope(scope); err != nil {
		return RepositoryView{}, err
	}
	if scope.Role == "operator" {
		authorized, err := service.operatorRepositoryAuthorized(ctx, repositoryID, scope)
		if err != nil {
			return RepositoryView{}, err
		}
		if !authorized {
			return RepositoryView{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
		}
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).Where("id = ?", repositoryID).First(&repository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return RepositoryView{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	} else if err != nil {
		return RepositoryView{}, fmt.Errorf("load visible backup repository: %w", err)
	}
	view, err := service.repositoryView(ctx, repository, scope)
	if err != nil {
		return RepositoryView{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryList, backupasset.AuditOutcomeSuccess, repository.ID, nil, "detail", nil)
	return view, nil
}

func validateVisibilityScope(scope VisibilityScope) error {
	switch scope.Role {
	case "admin":
		return nil
	case "operator":
		if scope.UserID != 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: repository visibility scope", backupasset.ErrForbidden)
}

func normalizeRepositoryPageLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: repository page limit", backupasset.ErrInvalidState)
	}
	if limit == 0 {
		limit = defaultRepositoryPageLimit
	}
	if limit > maxRepositoryPageLimit {
		limit = maxRepositoryPageLimit
	}
	return limit, nil
}

type repositoryCursor struct {
	CreatedAt    time.Time
	RepositoryID string
}

type repositoryCursorClaims struct {
	Version      int    `json:"version"`
	KeyVersion   int    `json:"key_version"`
	Role         string `json:"role"`
	UserID       uint   `json:"user_id"`
	CreatedAtNS  int64  `json:"created_at_ns"`
	RepositoryID string `json:"repository_id"`
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

type repositoryCursorEnvelope struct {
	Claims    repositoryCursorClaims `json:"claims"`
	Signature string                 `json:"signature"`
}

func (service *Service) encodeRepositoryCursor(ctx context.Context, scope VisibilityScope, createdAt time.Time, repositoryID string) (string, error) {
	if service.keyring == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return "", fmt.Errorf("%w: repository cursor signing unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.Ensure(ctx, backupasset.KeyDomainCursorSigning)
	if err != nil {
		return "", fmt.Errorf("sign repository cursor: %w", err)
	}
	now := service.utcNow()
	claims := repositoryCursorClaims{
		Version: repositoryCursorVersion, KeyVersion: material.Version, Role: scope.Role, UserID: scope.UserID,
		CreatedAtNS: createdAt.UTC().UnixNano(), RepositoryID: repositoryID, IssuedAt: now.Unix(), ExpiresAt: now.Add(repositoryCursorTTL).Unix(),
	}
	signature, err := signRepositoryCursor(material.Key, claims)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(repositoryCursorEnvelope{Claims: claims, Signature: signature})
	if err != nil {
		return "", fmt.Errorf("%w: encode repository cursor", backupasset.ErrInvalidState)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (service *Service) decodeRepositoryCursor(ctx context.Context, token string, scope VisibilityScope) (repositoryCursor, error) {
	if service.keyring == nil || token == "" || len(token) > repositoryCursorMaxBytes*2 {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(payload) == 0 || len(payload) > repositoryCursorMaxBytes {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	var envelope repositoryCursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	claims := envelope.Claims
	now := service.utcNow()
	if claims.Version != repositoryCursorVersion || claims.KeyVersion <= 0 || claims.Role != scope.Role || claims.UserID != scope.UserID ||
		backupasset.ValidateOpaqueID(claims.RepositoryID) != nil || claims.CreatedAtNS <= 0 || claims.IssuedAt > now.Unix() || claims.ExpiresAt <= now.Unix() || claims.ExpiresAt <= claims.IssuedAt {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.ByVersion(ctx, backupasset.KeyDomainCursorSigning, claims.KeyVersion)
	if err != nil {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	expected, err := signRepositoryCursor(material.Key, claims)
	if err != nil || !hmac.Equal([]byte(expected), []byte(envelope.Signature)) {
		return repositoryCursor{}, fmt.Errorf("%w: invalid repository cursor", backupasset.ErrInvalidState)
	}
	return repositoryCursor{CreatedAt: time.Unix(0, claims.CreatedAtNS).UTC(), RepositoryID: claims.RepositoryID}, nil
}

func signRepositoryCursor(key []byte, claims repositoryCursorClaims) (string, error) {
	if len(key) == 0 {
		return "", fmt.Errorf("%w: repository cursor key unavailable", backupasset.ErrInvalidState)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("%w: encode repository cursor claims", backupasset.ErrInvalidState)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(repositoryCursorDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

type repositoryVisibilityControl struct {
	ID        string
	CreatedAt time.Time
}

func (service *Service) listOperatorRepositories(
	ctx context.Context,
	limit int,
	cursor *repositoryCursor,
	scope VisibilityScope,
) ([]model.BackupRepository, bool, error) {
	const scanBudget = 2000
	visibleIDs := make([]string, 0, limit+1)
	var anchor *repositoryVisibilityControl
	if cursor != nil {
		anchor = &repositoryVisibilityControl{ID: cursor.RepositoryID, CreatedAt: cursor.CreatedAt}
	}
	scanned := 0
	hasCandidates := true
	for len(visibleIDs) < limit+1 && hasCandidates {
		remaining := scanBudget - scanned
		if remaining <= 0 {
			return nil, false, fmt.Errorf("%w: repository ownership scan budget", catalog.ErrOwnershipProjectionLimit)
		}
		chunkSize := min(remaining, max(50, limit*2))
		query := service.db.WithContext(ctx).Table("backup_repositories").Select("id", "created_at")
		if anchor != nil {
			query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
		}
		var candidates []repositoryVisibilityControl
		if err := query.Order("created_at DESC, id DESC").Limit(chunkSize + 1).Scan(&candidates).Error; err != nil {
			return nil, false, fmt.Errorf("list repository ownership controls: %w", err)
		}
		hasCandidates = len(candidates) > chunkSize
		if hasCandidates {
			candidates = candidates[:chunkSize]
		}
		if len(candidates) == 0 {
			break
		}
		for _, candidate := range candidates {
			authorized, err := service.operatorRepositoryAuthorized(ctx, candidate.ID, scope)
			if err != nil {
				return nil, false, err
			}
			if authorized {
				visibleIDs = append(visibleIDs, candidate.ID)
				if len(visibleIDs) == limit+1 {
					break
				}
			}
		}
		scanned += len(candidates)
		last := candidates[len(candidates)-1]
		anchor = &last
		if scanned >= scanBudget && hasCandidates && len(visibleIDs) < limit+1 {
			return nil, false, fmt.Errorf("%w: repository ownership scan budget", catalog.ErrOwnershipProjectionLimit)
		}
	}
	hasMore := len(visibleIDs) > limit
	if hasMore {
		visibleIDs = visibleIDs[:limit]
	}
	repositories := make([]model.BackupRepository, 0, len(visibleIDs))
	for _, repositoryID := range visibleIDs {
		var repository model.BackupRepository
		if err := service.db.WithContext(ctx).Where("id = ?", repositoryID).Take(&repository).Error; err != nil {
			return nil, false, fmt.Errorf("load authorized backup repository: %w", err)
		}
		repositories = append(repositories, repository)
	}
	return repositories, hasMore, nil
}

func (service *Service) operatorRepositoryAuthorized(ctx context.Context, repositoryID string, scope VisibilityScope) (bool, error) {
	if service.catalogOwnership == nil {
		return false, fmt.Errorf("%w: Catalog ownership unavailable", backupasset.ErrInvalidState)
	}
	var activeLinks int64
	if err := service.db.WithContext(ctx).Table("task_repository_links AS links").
		Joins("JOIN tasks AS link_tasks ON link_tasks.id = links.task_id AND link_tasks.archived_at IS NULL").
		Joins("JOIN nodes AS link_nodes ON link_nodes.id = link_tasks.node_id AND link_nodes.archived = ?", false).
		Joins("JOIN node_owners AS link_owners ON link_owners.node_id = link_nodes.id AND link_owners.user_id = ?", scope.UserID).
		Where("links.repository_id = ? AND links.task_id IS NOT NULL AND links.unlinked_at IS NULL", repositoryID).
		Count(&activeLinks).Error; err != nil {
		return false, fmt.Errorf("check repository current lineage ownership: %w", err)
	}
	if activeLinks > 0 {
		return true, nil
	}
	var pointIDs []string
	if err := service.db.WithContext(ctx).Table("recovery_points").Select("id").Where("repository_id = ?", repositoryID).
		Order("id ASC").Limit(2001).Scan(&pointIDs).Error; err != nil {
		return false, fmt.Errorf("load repository point ownership controls: %w", err)
	}
	if len(pointIDs) > 2000 {
		return false, fmt.Errorf("%w: repository point ownership budget", catalog.ErrOwnershipProjectionLimit)
	}
	authorized, err := service.catalogOwnership.AuthorizedPointIDs(ctx, catalog.AuthorizationScope{Role: scope.Role, UserID: scope.UserID}, pointIDs)
	if err != nil {
		return false, err
	}
	return len(authorized) > 0, nil
}

func (service *Service) repositoryView(ctx context.Context, repository model.BackupRepository, scope VisibilityScope) (RepositoryView, error) {
	dto, err := backupasset.ToRepositoryDTO(repository)
	if err != nil {
		return RepositoryView{}, err
	}
	lineages, err := service.loadLineages(ctx, repository.ID, scope)
	if err != nil {
		return RepositoryView{}, err
	}
	var activeBindings int64
	if err := service.db.WithContext(ctx).Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).
		Count(&activeBindings).Error; err != nil {
		return RepositoryView{}, fmt.Errorf("load repository access status: %w", err)
	}
	var summary catalog.RepositorySummaryDTO
	if service.catalogSummary != nil {
		summary, err = service.catalogSummary.RepositorySummary(ctx, repository.ID, catalog.AuthorizationScope{Role: scope.Role, UserID: scope.UserID})
		if err != nil {
			return RepositoryView{}, err
		}
	}
	return RepositoryView{RepositoryDTO: dto, AccessActive: activeBindings > 0, Lineages: lineages, Catalog: summary}, nil
}

func (service *Service) loadLineages(ctx context.Context, repositoryID string, scope VisibilityScope) ([]LineageSummary, error) {
	lineages := make([]LineageSummary, 0)
	linkQuery := service.db.WithContext(ctx).Table("task_repository_links AS links").Where("links.repository_id = ?", repositoryID)
	if scope.Role == "operator" {
		linkQuery = linkQuery.
			Joins("JOIN tasks AS link_tasks ON link_tasks.id = links.task_id AND link_tasks.archived_at IS NULL").
			Joins("JOIN nodes AS link_nodes ON link_nodes.id = link_tasks.node_id AND link_nodes.archived = ?", false).
			Joins("JOIN node_owners AS link_owners ON link_owners.node_id = link_nodes.id AND link_owners.user_id = ?", scope.UserID).
			Where("links.task_id IS NOT NULL AND links.unlinked_at IS NULL")
	}
	var links []struct {
		ID              string
		TaskID          *uint
		TaskName        string
		NodeID          uint
		NodeName        string
		PublicationMode string
		UnlinkedAt      *time.Time
	}
	if err := linkQuery.Select(`links.id, links.task_id, links.task_name_snapshot AS task_name,
		links.node_id_snapshot AS node_id, links.node_name_snapshot AS node_name,
		links.publication_mode, links.unlinked_at`).
		Order("links.created_at ASC, links.id ASC").Scan(&links).Error; err != nil {
		return nil, fmt.Errorf("load repository Task lineages: %w", err)
	}
	for _, link := range links {
		lineages = append(lineages, LineageSummary{
			Source: lineageSourceTaskLink, TaskRepositoryLinkID: link.ID, TaskID: link.TaskID, TaskName: link.TaskName,
			NodeID: link.NodeID, NodeName: link.NodeName, PublicationMode: link.PublicationMode, Active: link.UnlinkedAt == nil,
		})
	}

	pointQuery := service.db.WithContext(ctx).Table("recovery_points AS points").Where("points.repository_id = ?", repositoryID)
	if scope.Role == "operator" {
		if service.catalogOwnership == nil {
			return nil, fmt.Errorf("%w: Catalog ownership unavailable", backupasset.ErrInvalidState)
		}
		var candidateIDs []string
		if err := service.db.WithContext(ctx).Table("recovery_points").Select("id").Where("repository_id = ?", repositoryID).
			Order("id ASC").Limit(2001).Scan(&candidateIDs).Error; err != nil {
			return nil, fmt.Errorf("load repository lineage ownership controls: %w", err)
		}
		if len(candidateIDs) > 2000 {
			return nil, fmt.Errorf("%w: repository lineage ownership budget", catalog.ErrOwnershipProjectionLimit)
		}
		authorizedIDs, err := service.catalogOwnership.AuthorizedPointIDs(
			ctx, catalog.AuthorizationScope{Role: scope.Role, UserID: scope.UserID}, candidateIDs,
		)
		if err != nil {
			return nil, err
		}
		if len(authorizedIDs) == 0 {
			pointQuery = pointQuery.Where("1 = 0")
		} else {
			pointQuery = pointQuery.Where("points.id IN ?", authorizedIDs)
		}
	}
	var points []struct {
		ID        string
		TaskID    *uint
		TaskName  string
		NodeID    uint
		NodeName  string
		State     string
		Semantics string
	}
	if err := pointQuery.Select(`points.id, points.producing_task_id AS task_id,
		points.producing_task_name_snapshot AS task_name, points.producing_node_id_snapshot AS node_id,
		points.producing_node_name_snapshot AS node_name, points.state, points.semantics`).
		Order("points.created_at ASC, points.id ASC").Scan(&points).Error; err != nil {
		return nil, fmt.Errorf("load repository RecoveryPoint lineages: %w", err)
	}
	for _, point := range points {
		lineages = append(lineages, LineageSummary{
			Source: lineageSourceRecoveryPoint, TaskID: point.TaskID, TaskName: point.TaskName, NodeID: point.NodeID,
			NodeName: point.NodeName, RecoveryPointID: point.ID, RecoveryPointState: point.State,
			PointSemantics: point.Semantics, Active: true,
		})
	}
	return lineages, nil
}

type managedRsyncPointReadAdapter interface {
	ListPoints(context.Context, provider.ReadSnapshot, provider.PageRequest) (provider.NativePointPage, error)
	ListEntries(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error)
	StatEntry(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator) (provider.Entry, error)
	OpenSequential(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error)
	OpenRange(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error)
}

// RecoveryRsyncSourceAuthorityObservation is the closed Repository-owned
// source product consumed by Recovery authority composition. It carries only
// current scalar evidence; filesystem locators remain private to the source.
type RecoveryRsyncSourceAuthorityObservation struct {
	Provider                     backupasset.ProviderKind `json:"-"`
	RepositoryID                 string                   `json:"-"`
	RecoveryPointID              string                   `json:"-"`
	CatalogGenerationID          string                   `json:"-"`
	SourceRevisionDigest         string                   `json:"-"`
	ManifestDigest               string                   `json:"-"`
	RepositoryCapabilityRevision int                      `json:"-"`
	CapabilityRevision           int                      `json:"-"`
	SourceAccessIdentity         string                   `json:"-"`
	SourceFingerprint            string                   `json:"-"`
	ManagedRootIdentity          string                   `json:"-"`
	RepositoryBindingRevision    string                   `json:"-"`
	ProvenanceRevision           string                   `json:"-"`
}

func (RecoveryRsyncSourceAuthorityObservation) String() string {
	return "RecoveryRsyncSourceAuthorityObservation{redacted}"
}

func (RecoveryRsyncSourceAuthorityObservation) GoString() string {
	return "RecoveryRsyncSourceAuthorityObservation{redacted}"
}

// RecoveryRsyncSourceAuthority admits only the Provider-aware managed Rsync
// resolver. It remains capability-unavailable after exact source validation
// until an owning observer supplies purpose-exact source namespace evidence;
// Restic and Rclone have no equivalent exact source boundary yet.
type RecoveryRsyncSourceAuthority interface {
	ObserveRecoverySource(
		context.Context,
		provider.RecoverySourceAuthorityRequest,
	) (provider.RsyncRestoreSource, RecoveryRsyncSourceAuthorityObservation, error)
	RevalidateRecoverySourceAuthorityTx(
		context.Context,
		*gorm.DB,
		provider.RecoverySourceAuthorityRequest,
		RecoveryRsyncSourceAuthorityObservation,
	) error
}

// RecoverySourceNamespaceRequest is the Repository-owned scalar handoff to a
// runtime-injected namespace observer. Repository deliberately does not
// depend on the Recovery package; the runtime composition root translates
// this request into Recovery's private authority contract.
type RecoverySourceNamespaceRequest struct {
	SourceRef                 provider.RsyncRestoreSourceRef `json:"-"`
	ProducingTaskID           uint                           `json:"-"`
	RepositoryBindingRevision string                         `json:"-"`
	ProvenanceRevision        string                         `json:"-"`
}

func (RecoverySourceNamespaceRequest) String() string {
	return "RecoverySourceNamespaceRequest{redacted}"
}

func (RecoverySourceNamespaceRequest) GoString() string {
	return "RecoverySourceNamespaceRequest{redacted}"
}

// RecoverySourceNamespaceAuthority transfers one pinned source to the
// Recovery-owned observer without reversing the Repository -> Recovery
// package dependency. Ownership transfers when this method is invoked.
type RecoverySourceNamespaceAuthority interface {
	ObserveRecoverySourceNamespace(
		context.Context,
		RecoverySourceNamespaceRequest,
		provider.RsyncRestoreSource,
	) (provider.RsyncRestoreSource, error)
}

// ManagedRsyncPointReadSession owns the only internal path to a committed
// managed Rsync tree. It deliberately exposes provider-level opaque locators
// only; roots, marker material, and point locators remain private to this
// package. Its admission token remains active until every returned read handle
// has been closed.
type ManagedRsyncPointReadSession struct {
	adapter  managedRsyncPointReadAdapter
	snapshot provider.ReadSnapshot
	point    provider.PointLocator
	token    publication.AdmissionToken

	mu            sync.Mutex
	closed        bool
	active        int
	tokenReleased bool
}

// ResolveRsyncRestoreSource is the Repository-owned boundary for resolving a
// portable Recovery source ref. The ref intentionally has no caller-supplied
// Task ID, root, locator, or filesystem path; later resolution derives those
// facts from durable RecoveryPoint state.
func (service *Service) ResolveRsyncRestoreSource(ctx context.Context, ref provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
	if service == nil || service.db == nil ||
		backupasset.ValidateOpaqueID(ref.PlanID) != nil ||
		backupasset.ValidateOpaqueID(ref.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(ref.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(ref.CatalogGenerationID) != nil ||
		!isLowerHex64(ref.PlanBindingDigest) || !isLowerHex64(ref.SelectionDigest) ||
		!isLowerHex64(ref.SourceRevisionDigest) || !isLowerHex64(ref.ManifestDigest) {
		return nil, fmt.Errorf("%w: invalid Rsync restore source ref", provider.ErrInvalidRestoreRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return resolveRsyncRestoreSourceWithDependencies(ctx, ref, rsyncRestoreSourceResolverDependencies{
		loadSnapshot: func(ctx context.Context, ref provider.RsyncRestoreSourceRef) (rsyncRestoreDurableSnapshot, error) {
			var snapshot rsyncRestoreDurableSnapshot
			err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				loaded, err := service.loadRsyncRestoreDurableSnapshot(ctx, tx, ref)
				if err == nil {
					snapshot = loaded
				}
				return err
			})
			return snapshot, err
		},
		observeFilesystem:  observeRsyncRestoreSourceFilesystem,
		revalidateSnapshot: service.revalidateRsyncRestoreSnapshot,
		revalidateSource:   service.revalidateRsyncRestoreSource,
	})
}

func (service *Service) ObserveRecoverySource(
	ctx context.Context,
	request provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, RecoveryRsyncSourceAuthorityObservation, error) {
	if request.Provider != backupasset.ProviderRsync {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, fmt.Errorf("%w: Recovery source provider unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if service == nil {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, fmt.Errorf("%w: Recovery source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return observeRecoveryRsyncSourceWithDependencies(ctx, request, recoveryRsyncSourceAuthorityDependencies{
		resolve:    service.ResolveRsyncRestoreSource,
		capture:    service.captureRecoveryRsyncAuthoritySnapshot,
		revalidate: service.revalidateRecoveryRsyncAuthoritySnapshot,
		observe: func(ctx context.Context, namespaceRequest RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
			if service.recoverySourceNamespace == nil {
				_ = pinned.Close()
				return nil, fmt.Errorf("%w: Recovery source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
			}
			return service.recoverySourceNamespace.ObserveRecoverySourceNamespace(ctx, namespaceRequest, pinned)
		},
	})
}

type recoveryRsyncAuthoritySnapshot struct {
	producingTaskID           uint
	repositoryBindingRevision string
	provenanceRevision        string
	durable                   rsyncRestoreDurableSnapshot
	observation               RecoveryRsyncSourceAuthorityObservation
}

type recoveryRsyncSourceAuthorityDependencies struct {
	resolve    func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error)
	capture    func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error)
	observe    func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error)
	revalidate func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error
}

func observeRecoveryRsyncSourceWithDependencies(
	ctx context.Context,
	request provider.RecoverySourceAuthorityRequest,
	dependencies recoveryRsyncSourceAuthorityDependencies,
) (provider.RsyncRestoreSource, RecoveryRsyncSourceAuthorityObservation, error) {
	if request.Provider != backupasset.ProviderRsync || dependencies.resolve == nil || dependencies.capture == nil ||
		dependencies.observe == nil || dependencies.revalidate == nil {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, fmt.Errorf("%w: Recovery source provider unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pinned, err := dependencies.resolve(ctx, request.RsyncRef)
	if err != nil || pinned == nil {
		if pinned != nil {
			_ = pinned.Close()
		}
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	ownedPinned := true
	defer func() {
		if ownedPinned {
			_ = pinned.Close()
		}
	}()
	if err := pinned.Revalidate(ctx); err != nil {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	snapshot, err := dependencies.capture(ctx, request.RsyncRef)
	if err != nil || snapshot.producingTaskID == 0 || snapshot.repositoryBindingRevision == "" || snapshot.provenanceRevision == "" {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	observed, err := dependencies.observe(ctx, RecoverySourceNamespaceRequest{
		SourceRef: request.RsyncRef, ProducingTaskID: snapshot.producingTaskID,
		RepositoryBindingRevision: snapshot.repositoryBindingRevision,
		ProvenanceRevision:        snapshot.provenanceRevision,
	}, pinned)
	if err != nil || observed == nil {
		if observed != nil {
			ownedPinned = false
			_ = observed.Close()
		} else if err != nil {
			// The Recovery observer owns and closes the transferred pinned source
			// on every error path. A nil success product transfers nothing, so the
			// Repository defer remains the owner.
			ownedPinned = false
		}
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	ownedPinned = false
	if err := observed.Revalidate(ctx); err != nil {
		_ = observed.Close()
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	if err := dependencies.revalidate(ctx, request.RsyncRef, snapshot); err != nil {
		_ = observed.Close()
		return nil, RecoveryRsyncSourceAuthorityObservation{}, recoveryRsyncAuthorityError(ctx, err)
	}
	observation := recoveryRsyncAuthorityObservation(request.RsyncRef, snapshot)
	return observed, observation, nil
}

// RevalidateRecoverySourceAuthorityTx re-locks the complete Repository-owned
// source, capability, binding, and provenance product in the caller-owned
// transaction. It performs no filesystem or network work and never opens a
// nested transaction.
func (service *Service) RevalidateRecoverySourceAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	request provider.RecoverySourceAuthorityRequest,
	expected RecoveryRsyncSourceAuthorityObservation,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if service == nil || tx == nil || request.Provider != backupasset.ProviderRsync ||
		expected.Provider != backupasset.ProviderRsync || expected.RepositoryID != request.RsyncRef.RepositoryID ||
		expected.RecoveryPointID != request.RsyncRef.RecoveryPointID ||
		expected.CatalogGenerationID != request.RsyncRef.CatalogGenerationID ||
		expected.SourceRevisionDigest != request.RsyncRef.SourceRevisionDigest ||
		expected.ManifestDigest != request.RsyncRef.ManifestDigest ||
		expected.RepositoryCapabilityRevision <= 0 || expected.CapabilityRevision <= 0 ||
		expected.SourceAccessIdentity == "" || expected.SourceFingerprint == "" ||
		expected.ManagedRootIdentity == "" || expected.RepositoryBindingRevision == "" ||
		expected.ProvenanceRevision == "" {
		return invalidRsyncRestoreSourceRef()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := service.loadRecoveryRsyncAuthoritySnapshotTx(ctx, tx, request.RsyncRef)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return invalidRsyncRestoreSourceRef()
	}
	if !reflect.DeepEqual(recoveryRsyncAuthorityObservation(request.RsyncRef, current), expected) {
		return fmt.Errorf("%w: Recovery source authority changed", backupasset.ErrConflict)
	}
	return nil
}

func recoveryRsyncAuthorityObservation(
	ref provider.RsyncRestoreSourceRef,
	snapshot recoveryRsyncAuthoritySnapshot,
) RecoveryRsyncSourceAuthorityObservation {
	observation := snapshot.observation
	observation.Provider = backupasset.ProviderRsync
	observation.RepositoryID = ref.RepositoryID
	observation.RecoveryPointID = ref.RecoveryPointID
	observation.CatalogGenerationID = ref.CatalogGenerationID
	observation.SourceRevisionDigest = ref.SourceRevisionDigest
	observation.ManifestDigest = ref.ManifestDigest
	observation.RepositoryBindingRevision = snapshot.repositoryBindingRevision
	observation.ProvenanceRevision = snapshot.provenanceRevision
	return observation
}

func recoveryRsyncAuthorityError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		return fmt.Errorf("%w: Recovery source namespace authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if errors.Is(err, backupasset.ErrConflict) {
		return fmt.Errorf("%w: Recovery source binding changed", backupasset.ErrConflict)
	}
	return invalidRsyncRestoreSourceRef()
}

func observeRecoveryRsyncSourceWithResolver(
	ctx context.Context,
	request provider.RecoverySourceAuthorityRequest,
	resolve func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error),
) (provider.RsyncRestoreSource, RecoveryRsyncSourceAuthorityObservation, error) {
	if request.Provider != backupasset.ProviderRsync || resolve == nil {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, fmt.Errorf("%w: Recovery source provider unavailable", backupasset.ErrCapabilityUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	source, err := resolve(ctx, request.RsyncRef)
	if err != nil {
		if source != nil {
			_ = source.Close()
		}
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return nil, RecoveryRsyncSourceAuthorityObservation{}, contextErr
		}
		return nil, RecoveryRsyncSourceAuthorityObservation{}, invalidRsyncRestoreSourceRef()
	}
	if source == nil {
		return nil, RecoveryRsyncSourceAuthorityObservation{}, invalidRsyncRestoreSourceRef()
	}
	defer func() {
		_ = source.Close()
	}()
	if err := source.Revalidate(ctx); err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return nil, RecoveryRsyncSourceAuthorityObservation{}, contextErr
		}
		return nil, RecoveryRsyncSourceAuthorityObservation{}, invalidRsyncRestoreSourceRef()
	}
	return nil, RecoveryRsyncSourceAuthorityObservation{}, fmt.Errorf(
		"%w: Recovery source namespace authority unavailable",
		backupasset.ErrCapabilityUnavailable,
	)
}

func digestPinnedRsyncRestoreEntry(ctx context.Context, tree fileaccess.PinnedStrictTree, entry fileaccess.Entry) (string, error) {
	handle, stat, err := tree.OpenDeclaredRegular(ctx, entry)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, readErr := io.Copy(hash, handle)
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil || written != entry.Size || stat.Size != entry.Size || stat.SourceRevision != entry.SourceRevision {
		return "", fileaccess.ErrSourceChanged
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

type rsyncRestoreDeclaredEntry struct {
	restore provider.RestoreEntry
	locator fileaccess.Locator
	name    string
}

type rsyncRestoreDurableSnapshot struct {
	request                      provider.RsyncCommittedPointReadRequest
	declared                     []rsyncRestoreDeclaredEntry
	provider                     backupasset.ProviderKind
	repositoryID                 string
	recoveryPointID              string
	catalogGenerationID          string
	sourceRevisionDigest         string
	manifestDigest               string
	repositoryCapabilityRevision int
	capabilityRevision           int
	sourceAccessIdentity         string
	sourceFingerprint            string
	managedRootIdentity          string
}

type rsyncRestoreSourceResolverDependencies struct {
	loadSnapshot       func(context.Context, provider.RsyncRestoreSourceRef) (rsyncRestoreDurableSnapshot, error)
	observeFilesystem  func(context.Context, rsyncRestoreDurableSnapshot) (fileaccess.PinnedStrictTree, map[provider.RestoreEntry]fileaccess.Entry, []provider.RestoreEntry, error)
	revalidateSnapshot func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error
	revalidateSource   func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error
}

type repositoryRsyncRestoreSource struct {
	ref                provider.RsyncRestoreSourceRef
	snapshot           rsyncRestoreDurableSnapshot
	tree               fileaccess.PinnedStrictTree
	entries            map[provider.RestoreEntry]fileaccess.Entry
	ordered            []provider.RestoreEntry
	revalidateSnapshot func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error
	closeOnce          sync.Once
	closeErr           error
}

func (*repositoryRsyncRestoreSource) String() string {
	return "repositoryRsyncRestoreSource{redacted}"
}

func (*repositoryRsyncRestoreSource) GoString() string {
	return "repositoryRsyncRestoreSource{redacted}"
}

func (source *repositoryRsyncRestoreSource) OpenDeclaredRegular(ctx context.Context, requested provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	if source == nil || source.tree == nil || requested.Validate(requested.AssetRef.RecoveryPointID) != nil {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	entry, exists := source.entries[requested]
	if !exists {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	handle, stat, err := source.tree.OpenDeclaredRegular(ctx, entry)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	if stat.Size != requested.ExpectedSize || stat.SourceRevision != entry.SourceRevision {
		_ = handle.Close()
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	return handle, nil
}

func (source *repositoryRsyncRestoreSource) MaterializeDeclaredEntries(ctx context.Context, requested []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(source.entries) == 0 || len(source.ordered) != len(source.entries) || len(requested) != len(source.entries) {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	strictByBinding := make(map[rsyncRestoreEntryBinding]provider.RestoreEntry, len(source.entries))
	for strict := range source.entries {
		if strict.Validate(source.ref.RecoveryPointID) != nil {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
		binding := rsyncRestoreBindingForEntry(strict)
		if _, duplicate := strictByBinding[binding]; duplicate {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
		strictByBinding[binding] = strict
	}
	seen := make(map[rsyncRestoreEntryBinding]struct{}, len(requested))
	for _, entry := range requested {
		if entry.ExpectedDigest != "" {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
		binding := rsyncRestoreBindingForEntry(entry)
		if _, duplicate := seen[binding]; duplicate {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
		seen[binding] = struct{}{}
		if _, declared := strictByBinding[binding]; !declared {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
	}
	return append([]provider.RestoreEntry(nil), source.ordered...), nil
}

func (source *repositoryRsyncRestoreSource) Revalidate(ctx context.Context) error {
	if source == nil || source.tree == nil || source.revalidateSnapshot == nil {
		return provider.ErrRsyncRestoreSourceDrift
	}
	if err := source.revalidateSnapshot(ctx, source.ref, source.snapshot, source.entries); err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		return provider.ErrRsyncRestoreSourceDrift
	}
	if err := source.tree.Revalidate(ctx); err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		return provider.ErrRsyncRestoreSourceDrift
	}
	return nil
}

func (source *repositoryRsyncRestoreSource) Close() error {
	if source == nil || source.tree == nil {
		return nil
	}
	source.closeOnce.Do(func() {
		source.closeErr = source.tree.Close()
	})
	return source.closeErr
}

var _ provider.RsyncRestoreSource = (*repositoryRsyncRestoreSource)(nil)

func resolveRsyncRestoreSourceWithDependencies(
	ctx context.Context,
	ref provider.RsyncRestoreSourceRef,
	dependencies rsyncRestoreSourceResolverDependencies,
) (provider.RsyncRestoreSource, error) {
	if dependencies.loadSnapshot == nil || dependencies.observeFilesystem == nil ||
		dependencies.revalidateSnapshot == nil || dependencies.revalidateSource == nil {
		return nil, invalidRsyncRestoreSourceRef()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot, err := dependencies.loadSnapshot(ctx, ref)
	if err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		return nil, invalidRsyncRestoreSourceRef()
	}
	pinned, entries, ordered, observeErr := dependencies.observeFilesystem(ctx, snapshot)
	owned := pinned != nil
	defer func() {
		if owned {
			_ = pinned.Close()
		}
	}()
	if observeErr != nil || pinned == nil || len(entries) == 0 || len(ordered) != len(entries) {
		if contextErr := rsyncRestorePortContextError(ctx, observeErr); contextErr != nil {
			return nil, contextErr
		}
		return nil, invalidRsyncRestoreSourceRef()
	}
	source := &repositoryRsyncRestoreSource{
		ref: ref, snapshot: snapshot, tree: pinned, entries: entries, ordered: ordered,
		revalidateSnapshot: dependencies.revalidateSource,
	}
	if err := dependencies.revalidateSnapshot(ctx, ref, snapshot, entries); err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		return nil, invalidRsyncRestoreSourceRef()
	}
	if err := pinned.Revalidate(ctx); err != nil {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		return nil, invalidRsyncRestoreSourceRef()
	}
	owned = false
	return source, nil
}

func observeRsyncRestoreSourceFilesystem(
	ctx context.Context,
	snapshot rsyncRestoreDurableSnapshot,
) (fileaccess.PinnedStrictTree, map[provider.RestoreEntry]fileaccess.Entry, []provider.RestoreEntry, error) {
	access, err := provider.NewRsyncCommittedPointRuntimeAccess(ctx, snapshot.request)
	if err != nil {
		return nil, nil, nil, err
	}
	pinned, err := fileaccess.OpenPinnedStrictTree(ctx, access.Root, fileaccess.RootLocator())
	if err != nil {
		return nil, nil, nil, err
	}
	entries := make(map[provider.RestoreEntry]fileaccess.Entry, len(snapshot.declared))
	ordered := make([]provider.RestoreEntry, 0, len(snapshot.declared))
	for _, declaration := range snapshot.declared {
		entry, statErr := access.Tree.Lstat(ctx, access.Root, declaration.locator, fileaccess.ProviderPolicy)
		if statErr != nil || entry.Type != fileaccess.EntryFile || entry.Size != declaration.restore.ExpectedSize ||
			entry.Name != declaration.name || entry.SourceRevision == "" {
			return pinned, nil, nil, invalidRsyncRestoreSourceRef()
		}
		digest, digestErr := digestPinnedRsyncRestoreEntry(ctx, pinned, entry)
		if digestErr != nil || declaration.restore.ExpectedDigest != "" && declaration.restore.ExpectedDigest != digest {
			return pinned, nil, nil, invalidRsyncRestoreSourceRef()
		}
		declared := declaration.restore
		declared.ExpectedDigest = digest
		entries[declared] = entry
		ordered = append(ordered, declared)
	}
	return pinned, entries, ordered, nil
}

func (service *Service) loadRsyncRestoreDurableSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	ref provider.RsyncRestoreSourceRef,
) (rsyncRestoreDurableSnapshot, error) {
	plan, point, declared, err := loadRsyncRestoreSourceBinding(ctx, tx, ref)
	if err != nil {
		return rsyncRestoreDurableSnapshot{}, err
	}
	runtime, err := loadLockedExactManagedRsyncPublicationRuntime(ctx, tx, *point.ProducingTaskID)
	if err != nil || runtime.task.ID != *point.ProducingTaskID || runtime.repository.ID != plan.RepositoryID ||
		runtime.repository.ProviderKind != string(backupasset.ProviderRsync) || runtime.repository.RepositoryIdentity == nil ||
		runtime.repository.CapabilityRevision <= 0 {
		return rsyncRestoreDurableSnapshot{}, invalidRsyncRestoreSourceRef()
	}
	request, _, err := service.managedRsyncCommittedPointReadRequest(ctx, runtime, point)
	if err != nil {
		return rsyncRestoreDurableSnapshot{}, invalidRsyncRestoreSourceRef()
	}
	return rsyncRestoreDurableSnapshot{
		request: request, declared: declared, provider: backupasset.ProviderRsync,
		repositoryID: plan.RepositoryID, recoveryPointID: point.ID, catalogGenerationID: plan.CatalogGenerationID,
		sourceRevisionDigest: plan.SourceRevisionDigest, manifestDigest: point.ManifestDigest,
		repositoryCapabilityRevision: runtime.repository.CapabilityRevision, capabilityRevision: point.CapabilityRevision,
		sourceAccessIdentity: *runtime.repository.RepositoryIdentity, sourceFingerprint: point.SourceFingerprint,
		managedRootIdentity: runtime.binding.ManagedRootIdentityDigest,
	}, nil
}

func loadLockedExactManagedRsyncPublicationRuntime(
	ctx context.Context,
	tx *gorm.DB,
	taskID uint,
) (managedRsyncPublicationRuntime, error) {
	if tx == nil || taskID == 0 {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	var lockedTask model.Task
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND archived_at IS NULL", taskID).Limit(1).Find(&lockedTask)
	if loaded.Error != nil || loaded.RowsAffected != 1 || lockedTask.ID != taskID {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	var lockedLink model.TaskRepositoryLink
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("task_id = ? AND unlinked_at IS NULL", taskID).Limit(1).Find(&lockedLink)
	if loaded.Error != nil || loaded.RowsAffected != 1 || lockedLink.TaskID == nil || *lockedLink.TaskID != taskID {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	var lockedRepository model.BackupRepository
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", lockedLink.RepositoryID).Limit(1).Find(&lockedRepository)
	if loaded.Error != nil || loaded.RowsAffected != 1 || lockedRepository.ID != lockedLink.RepositoryID {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	var lockedBinding model.RepositoryAccessBinding
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", lockedRepository.ID, bindingStatusActive).Limit(1).Find(&lockedBinding)
	if loaded.Error != nil || loaded.RowsAffected != 1 || lockedBinding.RepositoryID != lockedRepository.ID {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	stored, err := decodeStoredBindingDocument(lockedBinding.EncryptedConfig)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil || stored.ManagedRcloneV3 != nil {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	lockedRuntime := managedRsyncPublicationRuntime{
		repository: lockedRepository, task: lockedTask, link: lockedLink, binding: *stored.ManagedRsyncV2,
	}
	validated, err := loadExactManagedRsyncPublicationRuntime(ctx, tx, taskID)
	if err != nil || !reflect.DeepEqual(validated, lockedRuntime) {
		return managedRsyncPublicationRuntime{}, invalidRsyncRestoreSourceRef()
	}
	return lockedRuntime, nil
}

func (service *Service) revalidateRsyncRestoreSnapshot(
	ctx context.Context,
	ref provider.RsyncRestoreSourceRef,
	snapshot rsyncRestoreDurableSnapshot,
	expected map[provider.RestoreEntry]fileaccess.Entry,
) error {
	if service == nil || service.db == nil || len(expected) == 0 {
		return invalidRsyncRestoreSourceRef()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := service.loadRsyncRestoreDurableSnapshot(ctx, tx, ref)
		if err != nil || !reflect.DeepEqual(current, snapshot) || len(current.declared) != len(expected) {
			return invalidRsyncRestoreSourceRef()
		}
		expectedBindings := make(map[rsyncRestoreEntryBinding]string, len(expected))
		for entry := range expected {
			binding := rsyncRestoreBindingForEntry(entry)
			if entry.Validate(ref.RecoveryPointID) != nil {
				return invalidRsyncRestoreSourceRef()
			}
			expectedBindings[binding] = entry.ExpectedDigest
		}
		for _, declaration := range current.declared {
			digest, exists := expectedBindings[rsyncRestoreBindingForEntry(declaration.restore)]
			if !exists || declaration.restore.ExpectedDigest != "" && declaration.restore.ExpectedDigest != digest {
				return invalidRsyncRestoreSourceRef()
			}
		}
		return nil
	})
}

func (service *Service) revalidateRsyncRestoreSource(
	ctx context.Context,
	ref provider.RsyncRestoreSourceRef,
	snapshot rsyncRestoreDurableSnapshot,
	expected map[provider.RestoreEntry]fileaccess.Entry,
) error {
	if service == nil || service.db == nil || len(expected) == 0 {
		return invalidRsyncRestoreSourceRef()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var current rsyncRestoreDurableSnapshot
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded, err := service.loadRsyncRestoreDurableSnapshot(ctx, tx, ref)
		if err != nil || !reflect.DeepEqual(loaded, snapshot) {
			return invalidRsyncRestoreSourceRef()
		}
		current = loaded
		return nil
	}); err != nil {
		return err
	}
	access, err := provider.NewRsyncCommittedPointRuntimeAccess(ctx, current.request)
	if err != nil || access.SourceRevision != current.sourceFingerprint {
		if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
			return contextErr
		}
		return invalidRsyncRestoreSourceRef()
	}
	return service.revalidateRsyncRestoreSnapshot(ctx, ref, snapshot, expected)
}

func (service *Service) captureRecoveryRsyncAuthoritySnapshot(
	ctx context.Context,
	ref provider.RsyncRestoreSourceRef,
) (recoveryRsyncAuthoritySnapshot, error) {
	if service == nil || service.db == nil {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot recoveryRsyncAuthoritySnapshot
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded, err := service.loadRecoveryRsyncAuthoritySnapshotTx(ctx, tx, ref)
		if err == nil {
			snapshot = loaded
		}
		return err
	})
	return snapshot, err
}

func (service *Service) revalidateRecoveryRsyncAuthoritySnapshot(
	ctx context.Context,
	ref provider.RsyncRestoreSourceRef,
	expected recoveryRsyncAuthoritySnapshot,
) error {
	if service == nil || service.db == nil || expected.producingTaskID == 0 ||
		expected.repositoryBindingRevision == "" || expected.provenanceRevision == "" {
		return invalidRsyncRestoreSourceRef()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := service.loadRecoveryRsyncAuthoritySnapshotTx(ctx, tx, ref)
		if err != nil || !reflect.DeepEqual(current, expected) {
			return invalidRsyncRestoreSourceRef()
		}
		return nil
	})
}

func (service *Service) loadRecoveryRsyncAuthoritySnapshotTx(
	ctx context.Context,
	tx *gorm.DB,
	ref provider.RsyncRestoreSourceRef,
) (recoveryRsyncAuthoritySnapshot, error) {
	if service == nil || tx == nil {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}
	durable, err := service.loadRsyncRestoreDurableSnapshot(ctx, tx, ref)
	if err != nil {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	var point model.RecoveryPoint
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ?", ref.RecoveryPointID, ref.RepositoryID).Limit(1).Find(&point)
	if loaded.Error != nil || loaded.RowsAffected != 1 || point.ProducingTaskID == nil || *point.ProducingTaskID == 0 {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	var task model.Task
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND archived_at IS NULL", *point.ProducingTaskID).Limit(1).Find(&task)
	if loaded.Error != nil || loaded.RowsAffected != 1 || task.ID != *point.ProducingTaskID || task.NodeID == 0 ||
		bindingProviderForTask(task) != backupasset.ProviderRsync {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	var link model.TaskRepositoryLink
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("task_id = ? AND repository_id = ? AND unlinked_at IS NULL", task.ID, ref.RepositoryID).Limit(1).Find(&link)
	if loaded.Error != nil || loaded.RowsAffected != 1 || link.TaskID == nil || *link.TaskID != task.ID ||
		link.NodeIDSnapshot != task.NodeID {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	var repository model.BackupRepository
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", ref.RepositoryID).Limit(1).Find(&repository)
	if loaded.Error != nil || loaded.RowsAffected != 1 || repository.RepositoryIdentity == nil ||
		repository.ProviderKind != string(backupasset.ProviderRsync) || repository.ID != durable.repositoryID ||
		repository.CapabilityRevision != durable.repositoryCapabilityRevision {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	var binding model.RepositoryAccessBinding
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", ref.RepositoryID, bindingStatusActive).Limit(1).Find(&binding)
	if loaded.Error != nil || loaded.RowsAffected != 1 || binding.RepositoryID != repository.ID ||
		binding.EncryptedConfig == "" {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil || stored.ManagedRcloneV3 != nil {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}
	document := *stored.ManagedRsyncV2
	if document.RollbackPrepared || validateManagedRsyncBindingAssociation(document, managedRsyncBindingAssociation{
		Task: task, Link: link, RootMarkerDigest: document.RootMarkerDigest,
	}) != nil {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}
	expectedIdentity, err := managedRsyncRepositoryIdentity(document)
	if err != nil || *repository.RepositoryIdentity != expectedIdentity ||
		document.ManagedRootIdentityDigest != durable.managedRootIdentity {
		return recoveryRsyncAuthoritySnapshot{}, invalidRsyncRestoreSourceRef()
	}

	repositoryBindingRevision := repositoryRecoveryAuthorityRevision(
		"xirang/repository/recovery-source-binding/v1",
		repository.ID, repository.ProviderKind, *repository.RepositoryIdentity, repository.DisplayName,
		repository.Description, repository.VersionMode, repository.Status,
		strconv.Itoa(repository.CapabilityRevision), repository.CapabilitiesJSON, repository.ImmutabilityLevel,
		repositoryAuthorityTime(repository.LastSeenAt), repositoryAuthorityTime(repository.LastReconciledAt),
		repository.CreatedAt.UTC().Format(time.RFC3339Nano), repository.UpdatedAt.UTC().Format(time.RFC3339Nano),
		link.ID, strconv.FormatUint(uint64(task.ID), 10), link.RepositoryID, link.TaskNameSnapshot,
		strconv.FormatUint(uint64(link.NodeIDSnapshot), 10), link.NodeNameSnapshot, link.PublicationMode,
		link.EncryptedLegacyLocator, link.LinkedAt.UTC().Format(time.RFC3339Nano), repositoryAuthorityTime(link.UnlinkedAt),
		link.CreatedAt.UTC().Format(time.RFC3339Nano), link.UpdatedAt.UTC().Format(time.RFC3339Nano),
		binding.ID, binding.RepositoryID, binding.BindingKind, binding.EncryptedConfig, binding.ConfigFingerprint,
		binding.Status, repositoryAuthorityTime(binding.RevokedAt), binding.CreatedAt.UTC().Format(time.RFC3339Nano),
		binding.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	provenanceRevision := repositoryRecoveryAuthorityRevision(
		"xirang/repository/recovery-source-provenance/v1",
		repositoryBindingRevision, ref.PlanID, ref.PlanBindingDigest, ref.RepositoryID, ref.RecoveryPointID,
		ref.CatalogGenerationID, ref.SelectionDigest, ref.SourceRevisionDigest, ref.ManifestDigest,
		point.ID, point.RepositoryID, repositoryAuthorityUint(point.ProducingTaskID), repositoryAuthorityUint(point.ProducingTaskRunID),
		point.ProducingTaskNameSnapshot, strconv.FormatUint(uint64(point.ProducingNodeIDSnapshot), 10), point.ProducingNodeNameSnapshot,
		point.LineageJSON, point.EncryptedProviderLocator, point.EncryptedRollbackLocator, point.Semantics, point.State,
		repositoryAuthorityTime(point.CapturedAt), repositoryAuthorityTime(point.CommittedAt), repositoryAuthorityTime(point.ObservedAt),
		point.SourceFingerprint, point.ManifestDigestAlgorithm, point.ManifestDigest, strconv.FormatInt(point.EntryCount, 10),
		strconv.FormatInt(point.LogicalBytes, 10), point.ConsistencyJSON, point.FidelityJSON,
		strconv.Itoa(point.CapabilityRevision), point.CapabilitiesJSON, point.ImmutabilityLevel, point.PhysicalAvailability,
		point.HoldState, repositoryAuthorityTime(point.HoldUntil), repositoryAuthorityTime(point.RetentionUntil),
		repositoryAuthorityString(point.RetirementReason), repositoryAuthorityTime(point.RetiredAt),
		point.CreatedAt.UTC().Format(time.RFC3339Nano), point.UpdatedAt.UTC().Format(time.RFC3339Nano),
		durable.catalogGenerationID, durable.sourceRevisionDigest, durable.manifestDigest,
		strconv.Itoa(durable.repositoryCapabilityRevision), strconv.Itoa(durable.capabilityRevision),
		durable.sourceAccessIdentity, durable.sourceFingerprint, durable.managedRootIdentity,
	)
	return recoveryRsyncAuthoritySnapshot{
		producingTaskID: task.ID, repositoryBindingRevision: repositoryBindingRevision,
		provenanceRevision: provenanceRevision, durable: durable,
		observation: RecoveryRsyncSourceAuthorityObservation{
			RepositoryCapabilityRevision: repository.CapabilityRevision,
			CapabilityRevision:           point.CapabilityRevision,
			SourceAccessIdentity:         durable.sourceAccessIdentity,
			SourceFingerprint:            point.SourceFingerprint,
			ManagedRootIdentity:          document.ManagedRootIdentityDigest,
		},
	}, nil
}

func repositoryRecoveryAuthorityRevision(domain string, values ...string) string {
	hash := sha256.New()
	var size [8]byte
	for _, value := range append([]string{domain}, values...) {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = io.WriteString(hash, value)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func repositoryAuthorityTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func repositoryAuthorityUint(value *uint) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}

func repositoryAuthorityString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type rsyncRestoreEntryBinding struct {
	AssetRef           backupasset.AssetRef
	Type               backupasset.CatalogEntryType
	ExpectedSize       int64
	TargetObjectDigest string
}

func rsyncRestoreBindingForEntry(entry provider.RestoreEntry) rsyncRestoreEntryBinding {
	return rsyncRestoreEntryBinding{
		AssetRef: entry.AssetRef, Type: entry.Type, ExpectedSize: entry.ExpectedSize,
		TargetObjectDigest: entry.TargetObjectDigest,
	}
}

func loadRsyncRestoreSourceBinding(
	ctx context.Context,
	tx *gorm.DB,
	ref provider.RsyncRestoreSourceRef,
) (model.BackupAssetRecoveryPlan, model.RecoveryPoint, []rsyncRestoreDeclaredEntry, error) {
	var plan model.BackupAssetRecoveryPlan
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ref.PlanID).First(&plan).Error; err != nil ||
		plan.RepositoryID != ref.RepositoryID || plan.RecoveryPointID != ref.RecoveryPointID ||
		plan.CatalogGenerationID != ref.CatalogGenerationID || plan.BindingDigest != ref.PlanBindingDigest ||
		plan.SelectionDigest != ref.SelectionDigest || plan.SourceRevisionDigest != ref.SourceRevisionDigest ||
		plan.SourceRevisionKind != rsyncRestoreImmutableSource || plan.ObservationFingerprint != "" ||
		plan.ImmutableManifestDigest != ref.ManifestDigest || !isLowerHex64(plan.ImmutableLocatorDigest) {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	var point model.RecoveryPoint
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ?", ref.RecoveryPointID, ref.RepositoryID).First(&point).Error; err != nil ||
		point.ProducingTaskID == nil || point.ManifestDigest != ref.ManifestDigest ||
		plan.EncryptedSourceLocator != point.EncryptedProviderLocator ||
		backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointCommitted ||
		(point.Semantics != string(backupasset.PointXirangManifest) && point.Semantics != string(backupasset.PointImportedBaseline)) {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	immutableLocatorDigest, err := publication.ImmutableLocatorDigest(
		plan.RepositoryID, backupasset.ProviderRsync, point.ID, point.EncryptedProviderLocator,
	)
	if err != nil || plan.ImmutableLocatorDigest != immutableLocatorDigest {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	var generation model.CatalogGeneration
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND recovery_point_id = ? AND state = ? AND is_active = ?", ref.CatalogGenerationID, ref.RecoveryPointID, catalog.GenerationComplete, true).
		First(&generation).Error; err != nil || generation.SourceFingerprint != point.SourceFingerprint ||
		generation.ManifestID == nil || backupasset.ValidateOpaqueID(*generation.ManifestID) != nil ||
		generation.ExpectedDigest != point.ManifestDigest || !isLowerHex64(generation.WrittenDigest) ||
		generation.ExpectedEntryCount != point.EntryCount || generation.WrittenEntryCount != point.EntryCount {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	var manifest model.RecoveryPointManifest
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND recovery_point_id = ? AND is_active = ?", *generation.ManifestID, point.ID, true).
		First(&manifest).Error; err != nil || manifest.Revision <= 0 || manifest.DigestAlgorithm != "sha256" ||
		manifest.Digest != point.ManifestDigest || manifest.Completeness != string(backupasset.ManifestComplete) ||
		manifest.EntryCount != point.EntryCount || manifest.LogicalBytes != point.LogicalBytes {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	declared, err := lockExactRsyncRestorePlanItems(ctx, tx, plan, point, generation)
	if err != nil {
		return model.BackupAssetRecoveryPlan{}, model.RecoveryPoint{}, nil, invalidRsyncRestoreSourceRef()
	}
	return plan, point, declared, nil
}

func lockExactRsyncRestorePlanItems(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
	point model.RecoveryPoint,
	generation model.CatalogGeneration,
) ([]rsyncRestoreDeclaredEntry, error) {
	var items []model.BackupAssetRecoveryPlanItem
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("plan_id = ?", plan.ID).Order("ordinal ASC, id ASC").Find(&items).Error; err != nil || len(items) == 0 {
		return nil, invalidRsyncRestoreSourceRef()
	}

	entryIDs := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for ordinal, item := range items {
		if item.PlanID != plan.ID || item.Ordinal != ordinal || item.RecoveryPointID != point.ID ||
			item.CatalogGenerationID != generation.ID || backupasset.ValidateAssetRef(backupasset.AssetRef{
			RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID,
		}) != nil || item.EntryType != string(backupasset.CatalogEntryFile) || item.SourceFingerprint != "" ||
			!isLowerHex64(item.RelativePathDigest) {
			return nil, invalidRsyncRestoreSourceRef()
		}
		if _, exists := seen[item.EntryID]; exists {
			return nil, invalidRsyncRestoreSourceRef()
		}
		seen[item.EntryID] = struct{}{}
		entryIDs = append(entryIDs, item.EntryID)
	}

	var entries []model.CatalogEntry
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", generation.ID, point.ID, entryIDs).
		Find(&entries).Error; err != nil || len(entries) != len(items) {
		return nil, invalidRsyncRestoreSourceRef()
	}
	entriesByID := make(map[string]model.CatalogEntry, len(entries))
	for _, entry := range entries {
		if entry.GenerationID != generation.ID || entry.RecoveryPointID != point.ID ||
			backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID}) != nil ||
			entry.EntryType != string(backupasset.CatalogEntryFile) || entry.Size < 0 || entry.NormalizedPath == "" {
			return nil, invalidRsyncRestoreSourceRef()
		}
		entriesByID[entry.EntryID] = entry
	}
	declared := make([]rsyncRestoreDeclaredEntry, 0, len(items))
	for _, item := range items {
		entry, found := entriesByID[item.EntryID]
		if !found || entry.EntryType != item.EntryType ||
			item.RelativePathDigest != publication.RecoveryPlanItemPathDigest(
				plan.RepositoryID, point.ID, generation.ID, entry.EntryID, entry.NormalizedPath,
			) || entry.SecurityState != "sealed" {
			return nil, invalidRsyncRestoreSourceRef()
		}
		switch entry.FingerprintStrength {
		case string(catalog.FingerprintStrong):
			if !isLowerHex64(entry.Fingerprint) {
				return nil, invalidRsyncRestoreSourceRef()
			}
		case string(catalog.FingerprintNone):
			if entry.Fingerprint != "" {
				return nil, invalidRsyncRestoreSourceRef()
			}
		default:
			return nil, invalidRsyncRestoreSourceRef()
		}
		providerLocator, err := decodeContentEntryLocator(entry.EncryptedProviderLocator)
		if err != nil || providerLocator.Native != entry.NormalizedPath {
			return nil, invalidRsyncRestoreSourceRef()
		}
		locator, err := fileaccess.ParseLocator(providerLocator.Native, fileaccess.ProviderPolicy)
		if err != nil {
			return nil, invalidRsyncRestoreSourceRef()
		}
		declared = append(declared, rsyncRestoreDeclaredEntry{
			restore: provider.RestoreEntry{
				AssetRef: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
				Type:     backupasset.CatalogEntryFile, ExpectedSize: entry.Size,
				ExpectedDigest: entry.Fingerprint, TargetObjectDigest: item.RelativePathDigest,
			},
			locator: locator,
			name:    entry.Name,
		})
	}
	return declared, nil
}

func invalidRsyncRestoreSourceRef() error {
	return fmt.Errorf("%w: Rsync restore source binding rejected", provider.ErrInvalidRestoreRequest)
}

// RsyncRestorePort is the Repository-owned concrete RestorePort. Provider
// owns only portable request/result contracts and the runner seam; this port
// resolves the scalar source ref into a pinned descriptor for each phase.
type RsyncRestorePort struct {
	resolver provider.RsyncRestoreSourceResolver
	writer   provider.RsyncTargetWriter
	runner   provider.RsyncRestoreRunner
}

func NewRsyncRestorePort(
	resolver provider.RsyncRestoreSourceResolver,
	writer provider.RsyncTargetWriter,
	runner provider.RsyncRestoreRunner,
) *RsyncRestorePort {
	return &RsyncRestorePort{resolver: resolver, writer: writer, runner: runner}
}

func (*RsyncRestorePort) ProviderKind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}

func (port *RsyncRestorePort) Preflight(ctx context.Context, request provider.RestorePreflightRequest) (evidence provider.RestorePreflightEvidence, err error) {
	if err = port.validateDependencies(); err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	now := time.Now().UTC()
	if err = request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	if err = request.Permit.ValidateAt(now, request.Request.Target); err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	intent, source, err := port.resolveIntent(ctx, request.Request)
	if err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			evidence = provider.RestorePreflightEvidence{}
			err = unavailableRsyncRestorePortError(ctx, closeErr)
		}
	}()
	runnerEvidence, runnerErr := port.runner.Preflight(ctx, provider.RsyncRestorePreflightCall{RsyncRestoreIntent: intent, Permit: request.Permit})
	if err = source.Revalidate(ctx); err != nil {
		return provider.RestorePreflightEvidence{}, sourceDriftRsyncRestorePortError(ctx, runnerErr, err)
	}
	if runnerErr != nil {
		return provider.RestorePreflightEvidence{}, unavailableRsyncRestorePortError(ctx, runnerErr)
	}
	if err = provider.ValidateRsyncRestoreRunnerObservation(request.Request, runnerEvidence); err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	evidence = provider.RestorePreflightEvidence{
		TargetBindingDigest: runnerEvidence.TargetBindingDigest,
		TargetRevision:      runnerEvidence.TargetRevision,
		Checkpoint:          runnerEvidence.Checkpoint,
		Operations:          append([]provider.RestoreEvidence(nil), runnerEvidence.Evidence...),
	}
	if err = evidence.ValidateFor(request.Request); err != nil {
		return provider.RestorePreflightEvidence{}, err
	}
	return evidence, nil
}

func (port *RsyncRestorePort) Execute(ctx context.Context, request provider.RestoreRequest, progress provider.RestoreProgress) (result provider.RestoreResult, err error) {
	if err = port.validateDependencies(); err != nil {
		return provider.RestoreResult{}, err
	}
	if err = request.ValidateRsyncResolutionIntent(); err != nil {
		return provider.RestoreResult{}, err
	}
	if err = request.MutationPermit.ValidateAt(time.Now().UTC(), request.Target, request.Fence); err != nil {
		return provider.RestoreResult{}, err
	}
	intent, source, err := port.resolveIntent(ctx, request)
	if err != nil {
		return provider.RestoreResult{}, err
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			result = provider.RestoreResult{}
			err = unavailableRsyncRestorePortError(ctx, closeErr)
		}
	}()
	runnerResult, runnerErr := port.runner.Execute(ctx, provider.RsyncRestoreExecuteCall{
		RsyncRestoreIntent: intent,
		Permit:             request.MutationPermit,
		Progress:           progress,
	})
	if err = source.Revalidate(ctx); err != nil {
		return provider.RestoreResult{}, sourceDriftRsyncRestorePortError(ctx, runnerErr, err)
	}
	if runnerErr != nil {
		return provider.RestoreResult{}, unavailableRsyncRestorePortError(ctx, runnerErr)
	}
	result = provider.RestoreResult{Checkpoint: runnerResult.Checkpoint, Evidence: append([]provider.RestoreEvidence(nil), runnerResult.Evidence...)}
	if err = result.ValidateFor(request); err != nil {
		return provider.RestoreResult{}, err
	}
	return result, nil
}

func (port *RsyncRestorePort) Verify(ctx context.Context, request provider.RestoreVerifyRequest) (result provider.RestoreVerifyResult, err error) {
	if err = port.validateDependencies(); err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	now := time.Now().UTC()
	if err = request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	if err = request.Permit.ValidateAt(now, request.Request.Target); err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	intent, source, err := port.resolveIntent(ctx, request.Request)
	if err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			result = provider.RestoreVerifyResult{}
			err = unavailableRsyncRestorePortError(ctx, closeErr)
		}
	}()
	runnerEvidence, runnerErr := port.runner.Verify(ctx, provider.RsyncRestoreVerifyCall{RsyncRestoreIntent: intent, Permit: request.Permit})
	if err = source.Revalidate(ctx); err != nil {
		return provider.RestoreVerifyResult{}, sourceDriftRsyncRestorePortError(ctx, runnerErr, err)
	}
	if runnerErr != nil {
		return provider.RestoreVerifyResult{}, unavailableRsyncRestorePortError(ctx, runnerErr)
	}
	if err = provider.ValidateRsyncRestoreRunnerObservation(request.Request, runnerEvidence); err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	result = provider.RestoreVerifyResult{Checkpoint: runnerEvidence.Checkpoint, Evidence: append([]provider.RestoreEvidence(nil), runnerEvidence.Evidence...)}
	if err = result.ValidateFor(request.Request); err != nil {
		return provider.RestoreVerifyResult{}, err
	}
	return result, nil
}

func (port *RsyncRestorePort) Reconcile(ctx context.Context, request provider.RestoreReconcileRequest) (result provider.RestoreReconcileResult, err error) {
	if err = port.validateDependencies(); err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	now := time.Now().UTC()
	if err = request.Request.ValidateRsyncResolutionIntent(); err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	if err = request.Permit.ValidateAt(now, request.Request.Target); err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	intent, source, err := port.resolveIntent(ctx, request.Request)
	if err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			result = provider.RestoreReconcileResult{}
			err = unavailableRsyncRestorePortError(ctx, closeErr)
		}
	}()
	runnerEvidence, runnerErr := port.runner.Reconcile(ctx, provider.RsyncRestoreReconcileCall{RsyncRestoreIntent: intent, Permit: request.Permit})
	if err = source.Revalidate(ctx); err != nil {
		return provider.RestoreReconcileResult{}, sourceDriftRsyncRestorePortError(ctx, runnerErr, err)
	}
	if runnerErr != nil {
		return provider.RestoreReconcileResult{}, unavailableRsyncRestorePortError(ctx, runnerErr)
	}
	if err = provider.ValidateRsyncRestoreRunnerObservation(request.Request, runnerEvidence); err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	result = provider.RestoreReconcileResult{Checkpoint: runnerEvidence.Checkpoint, Evidence: append([]provider.RestoreEvidence(nil), runnerEvidence.Evidence...)}
	if err = result.ValidateFor(request.Request); err != nil {
		return provider.RestoreReconcileResult{}, err
	}
	return result, nil
}

func (port *RsyncRestorePort) resolveIntent(ctx context.Context, request provider.RestoreRequest) (provider.RsyncRestoreIntent, provider.RsyncRestoreSource, error) {
	if err := request.ValidateRsyncResolutionIntent(); err != nil || request.Rsync == nil {
		return provider.RsyncRestoreIntent{}, nil, sourceDriftRsyncRestorePortError(ctx)
	}
	source, err := port.resolver.ResolveRsyncRestoreSource(ctx, request.Rsync.SourceRef)
	if err != nil || source == nil {
		return provider.RsyncRestoreIntent{}, nil, sourceResolutionRsyncRestorePortError(ctx, err)
	}
	if err := source.Revalidate(ctx); err != nil {
		_ = source.Close()
		return provider.RsyncRestoreIntent{}, nil, sourceDriftRsyncRestorePortError(ctx, err)
	}
	entries, err := source.MaterializeDeclaredEntries(ctx, request.Entries)
	if err != nil {
		_ = source.Close()
		return provider.RsyncRestoreIntent{}, nil, sourceDriftRsyncRestorePortError(ctx, err)
	}
	request.Entries = entries
	if err := request.ValidateIntent(); err != nil {
		_ = source.Close()
		return provider.RsyncRestoreIntent{}, nil, sourceDriftRsyncRestorePortError(ctx, err)
	}
	intent, err := provider.NewRsyncRestoreIntent(request, source, port.writer)
	if err != nil {
		_ = source.Close()
		return provider.RsyncRestoreIntent{}, nil, sourceDriftRsyncRestorePortError(ctx, err)
	}
	return intent, source, nil
}

func (port *RsyncRestorePort) validateDependencies() error {
	if port == nil || isNilRsyncRestoreDependency(port.resolver) || isNilRsyncRestoreDependency(port.writer) || isNilRsyncRestoreDependency(port.runner) {
		return unavailableRsyncRestorePortError(context.Background())
	}
	return nil
}

func isNilRsyncRestoreDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func sourceResolutionRsyncRestorePortError(ctx context.Context, err error) error {
	if contextErr := rsyncRestorePortContextError(ctx, err); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || errors.Is(err, provider.ErrInvalidRestoreRequest) ||
		errors.Is(err, fileaccess.ErrSourceChanged) || errors.Is(err, fileaccess.ErrStrictUnavailable) {
		return provider.ErrRsyncRestoreSourceDrift
	}
	return provider.ErrRsyncRestoreUnavailable
}

func sourceDriftRsyncRestorePortError(ctx context.Context, causes ...error) error {
	if contextErr := rsyncRestorePortContextError(ctx, causes...); contextErr != nil {
		return contextErr
	}
	return provider.ErrRsyncRestoreSourceDrift
}

func unavailableRsyncRestorePortError(ctx context.Context, causes ...error) error {
	if contextErr := rsyncRestorePortContextError(ctx, causes...); contextErr != nil {
		return contextErr
	}
	return provider.ErrRsyncRestoreUnavailable
}

func rsyncRestorePortContextError(ctx context.Context, causes ...error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for _, cause := range causes {
		if errors.Is(cause, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
	}
	return nil
}

var _ provider.RestorePort = (*RsyncRestorePort)(nil)

// BeginManagedRsyncPointRead reconstructs one exact committed managed Rsync
// tree. It never accepts a root, path, marker, or locator from its caller.
func (service *Service) BeginManagedRsyncPointRead(ctx context.Context, taskID uint, recoveryPointID string) (*ManagedRsyncPointReadSession, error) {
	return service.beginManagedRsyncPointRead(ctx, taskID, recoveryPointID, publication.OperationManagedRsyncPointRead)
}

func (service *Service) beginManagedRsyncPointRead(
	ctx context.Context,
	taskID uint,
	recoveryPointID string,
	operation publication.ResticOperation,
) (*ManagedRsyncPointReadSession, error) {
	if service == nil || service.db == nil || service.foundation == nil || service.admission == nil || service.keyring == nil ||
		taskID == 0 || backupasset.ValidateOpaqueID(recoveryPointID) != nil ||
		(operation != publication.OperationManagedRsyncPointRead && operation != publication.OperationContentRead) {
		return nil, fmt.Errorf("%w: managed Rsync point reader request is invalid", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := service.admission.Acquire(ctx, operation)
	if err != nil {
		return nil, err
	}
	session, err := service.beginManagedRsyncPointReadWithAdmission(ctx, taskID, recoveryPointID, token)
	if err != nil {
		_ = token.Close()
		return nil, err
	}
	return session, nil
}

// beginManagedRsyncPointReadWithAdmission borrows token on failure and transfers
// ownership to the returned session on success. Content uses this seam so one
// admission acquired before decrypted Catalog/access loads is not reacquired.
func (service *Service) beginManagedRsyncPointReadWithAdmission(
	ctx context.Context,
	taskID uint,
	recoveryPointID string,
	token publication.AdmissionToken,
) (*ManagedRsyncPointReadSession, error) {
	if service == nil || service.db == nil || service.foundation == nil || service.keyring == nil ||
		taskID == 0 || backupasset.ValidateOpaqueID(recoveryPointID) != nil || token == nil ||
		(token.Operation() != publication.OperationManagedRsyncPointRead && token.Operation() != publication.OperationContentRead) {
		return nil, fmt.Errorf("%w: managed Rsync admitted reader request is invalid", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if token.Mode() != publication.AdmissionManaged && token.Mode() != publication.AdmissionRollbackSafe {
		return nil, fmt.Errorf("%w: managed Rsync point reader is not admitted", backupasset.ErrForbidden)
	}

	runtime, err := loadExactManagedRsyncPublicationRuntime(ctx, service.db, taskID)
	if err != nil {
		return nil, err
	}
	var point model.RecoveryPoint
	if err := service.db.WithContext(ctx).Where("id = ? AND repository_id = ?", recoveryPointID, runtime.repository.ID).First(&point).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: managed Rsync recovery point", backupasset.ErrNotFound)
		}
		return nil, fmt.Errorf("load managed Rsync recovery point: %w", err)
	}
	if backupasset.RecoveryPointState(point.State) != backupasset.RecoveryPointCommitted {
		return nil, capabilityError(backupasset.CapabilityPointNotCommitted, "")
	}
	request, access, err := service.managedRsyncCommittedPointReadRequest(ctx, runtime, point)
	if err != nil {
		return nil, err
	}
	adapter, err := service.newManagedRsyncCommittedPointAdapter()
	if err != nil {
		return nil, err
	}
	runtimeAccess, err := provider.NewRsyncCommittedPointRuntimeAccess(ctx, request)
	if err != nil {
		return nil, mapManagedRsyncCommittedPointReadOpenError(ctx, err)
	}
	access.AdapterData = runtimeAccess
	snapshot := provider.ReadSnapshot{
		RepositoryID: runtime.repository.ID, CapabilityRevision: point.CapabilityRevision,
		SourceRevision: runtimeAccess.SourceRevision, Access: access,
	}
	points, err := adapter.ListPoints(ctx, snapshot, provider.PageRequest{Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(points.Items) != 1 || points.NextCursor != "" {
		return nil, fmt.Errorf("%w: committed Rsync point reader returned an invalid point set", backupasset.ErrInvalidState)
	}
	return &ManagedRsyncPointReadSession{adapter: adapter, snapshot: snapshot, point: points.Items[0].Locator, token: token}, nil
}

func mapManagedRsyncCommittedPointReadOpenError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		return capabilityError(backupasset.CapabilityProviderUnavailable, "")
	}
	return capabilityError(backupasset.CapabilityMutableSourceChanged, "")
}

func (service *Service) managedRsyncCommittedPointReadRequest(ctx context.Context, runtime managedRsyncPublicationRuntime, point model.RecoveryPoint) (provider.RsyncCommittedPointReadRequest, provider.AccessBinding, error) {
	if point.RepositoryID != runtime.repository.ID || point.ID == "" || backupasset.ValidateOpaqueID(point.ID) != nil ||
		point.ProducingTaskID == nil || *point.ProducingTaskID != runtime.task.ID || point.ProducingTaskRunID == nil ||
		point.CapabilityRevision <= 0 || point.CapturedAt == nil || point.CapturedAt.IsZero() || point.EntryCount < 0 || point.LogicalBytes < 0 ||
		point.ManifestDigestAlgorithm != "sha256" || !isLowerHex64(point.SourceFingerprint) || !isLowerHex64(point.ManifestDigest) {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point evidence is invalid", backupasset.ErrConflict)
	}
	if runtime.repository.CapabilityRevision != point.CapabilityRevision {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point capability revision changed", backupasset.ErrConflict)
	}
	semantics := backupasset.PointVersionSemantics(point.Semantics)
	if semantics != backupasset.PointXirangManifest && semantics != backupasset.PointImportedBaseline {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point semantics are invalid", backupasset.ErrConflict)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	if lineage.TaskID != runtime.task.ID || lineage.TaskRunID != *point.ProducingTaskRunID ||
		lineage.TaskRepositoryLinkID != runtime.link.ID || lineage.PublicationMode != runtime.link.PublicationMode {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point lineage changed", backupasset.ErrConflict)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	if consistency.Provider != backupasset.ProviderRsync || consistency.RepositoryIdentityDigest != runtime.binding.ManagedRootIdentityDigest ||
		consistency.CapabilityRevision != point.CapabilityRevision ||
		!isLowerHex64(consistency.ProviderCommitDigest) {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point consistency changed", backupasset.ErrConflict)
	}
	locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	if locator.RepositoryID != runtime.repository.ID || locator.RecoveryPointID != point.ID || locator.FinalComponent != point.ID ||
		attempt.RepositoryID != runtime.repository.ID || attempt.RecoveryPointID != point.ID || attempt.TaskID != runtime.task.ID ||
		attempt.TaskRunID != *point.ProducingTaskRunID || attempt.TaskRepositoryLinkID != runtime.link.ID ||
		attempt.PublicationMode != backupasset.TaskPublicationMode(runtime.link.PublicationMode) ||
		!lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) || runtime.binding.validateForAttempt(attempt) != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point locator changed", backupasset.ErrConflict)
	}
	markerKey, err := service.rsyncCommittedPointMarkerKey(ctx, runtime.repository.ID)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	if point.SourceFingerprint != managedRsyncSourceFingerprint(markerKey, runtime.binding, point.ID) {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, fmt.Errorf("%w: committed Rsync point source changed", backupasset.ErrConflict)
	}
	limits, err := service.managedRsyncManifestLimits()
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	salt, err := hexDecodeSalt(runtime.binding.IdentitySalt)
	if err != nil {
		return provider.RsyncCommittedPointReadRequest{}, provider.AccessBinding{}, err
	}
	request := provider.RsyncCommittedPointReadRequest{
		ManagedRoot: runtime.binding.ManagedRootLocator, MarkerKey: markerKey, Attempt: attempt,
		CommitMarkerDigest: locator.CommitMarkerDigest, SourceFingerprint: point.SourceFingerprint,
		ChildFenceDigest: locator.ChildFenceDigest, ManifestDigest: point.ManifestDigest,
		ManifestEntryCount: uint64(point.EntryCount), LogicalBytes: uint64(point.LogicalBytes),
		CapturedAt: point.CapturedAt.UTC(), Semantics: semantics, ManifestLimits: limits,
	}
	access := provider.AccessBinding{
		Provider: backupasset.ProviderRsync, RepositoryID: runtime.repository.ID, TaskID: runtime.task.ID,
		NodeID: runtime.binding.NodeID, IdentitySalt: salt,
	}
	return request, access, nil
}

func (service *Service) rsyncCommittedPointMarkerKey(ctx context.Context, repositoryID string) ([]byte, error) {
	if service == nil || service.keyring == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return nil, fmt.Errorf("%w: managed Rsync point marker key is unavailable", backupasset.ErrInvalidState)
	}
	material, err := service.keyring.Active(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil {
		return nil, fmt.Errorf("load managed Rsync point marker key: %w", err)
	}
	return rsyncOwnershipDigest(material.Key, "xirang.rsync.tree.marker-key.v1", repositoryID), nil
}

func (service *Service) managedRsyncManifestLimits() (provider.ManifestLimits, error) {
	if service == nil || service.foundation == nil {
		return provider.ManifestLimits{}, fmt.Errorf("%w: managed Rsync manifest limits are unavailable", backupasset.ErrInvalidState)
	}
	config, err := service.foundation.PublicationConfig()
	if err != nil {
		return provider.ManifestLimits{}, err
	}
	maxBytes := config.ManifestMaxBytes
	if maxBytes > provider.MaxRsyncTreeMetadataBytes {
		maxBytes = provider.MaxRsyncTreeMetadataBytes
	}
	return provider.ManifestLimits{
		Timeout: config.ManifestTimeout, MaxBytes: maxBytes, MaxEntries: config.ManifestMaxEntries,
		MaxRecordBytes: config.ManifestMaxRecordBytes, MaxDepth: config.ManifestMaxDepth,
	}, nil
}

func (service *Service) newManagedRsyncCommittedPointAdapter() (*provider.RsyncCommittedPointAdapter, error) {
	if service == nil || service.foundation == nil || service.keyring == nil {
		return nil, fmt.Errorf("%w: managed Rsync point reader dependencies are unavailable", backupasset.ErrInvalidState)
	}
	limitsSource := func() (provider.OperationLimits, error) {
		config, err := service.foundation.ProviderConfig()
		if err != nil {
			return provider.OperationLimits{}, err
		}
		return provider.NewMetadataOperationLimits(config.OperationTimeout, config.MetadataLimitBytes)
	}
	return provider.NewRsyncCommittedPointAdapterWithLimitsSource(
		provider.NewCursorCodec(service.keyring, service.now, managedRsyncReaderCursorTTL), limitsSource, managedRsyncReaderPageSize,
	)
}

func (session *ManagedRsyncPointReadSession) ListEntries(ctx context.Context, parent provider.EntryLocator, request provider.PageRequest) (provider.EntryPage, error) {
	if err := session.begin(); err != nil {
		return provider.EntryPage{}, err
	}
	defer session.end()
	return session.adapter.ListEntries(ctx, session.snapshot, session.point, parent, request)
}

func (session *ManagedRsyncPointReadSession) StatEntry(ctx context.Context, locator provider.EntryLocator) (provider.Entry, error) {
	if err := session.begin(); err != nil {
		return provider.Entry{}, err
	}
	defer session.end()
	return session.adapter.StatEntry(ctx, session.snapshot, session.point, locator)
}

func (session *ManagedRsyncPointReadSession) OpenSequential(ctx context.Context, locator provider.EntryLocator, request provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	if err := session.begin(); err != nil {
		return nil, provider.ContentStat{}, err
	}
	handle, stat, err := session.adapter.OpenSequential(ctx, session.snapshot, session.point, locator, request)
	if err != nil {
		session.end()
		return nil, provider.ContentStat{}, err
	}
	return &managedRsyncPointReadHandle{underlying: handle, session: session}, stat, nil
}

func (session *ManagedRsyncPointReadSession) OpenRange(ctx context.Context, locator provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	if err := session.begin(); err != nil {
		return nil, provider.ContentStat{}, err
	}
	handle, stat, err := session.adapter.OpenRange(ctx, session.snapshot, session.point, locator, byteRange)
	if err != nil {
		session.end()
		return nil, provider.ContentStat{}, err
	}
	return &managedRsyncPointReadHandle{underlying: handle, session: session}, stat, nil
}

func (session *ManagedRsyncPointReadSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	session.closed = true
	token := session.releaseTokenLocked()
	session.mu.Unlock()
	if token == nil {
		return nil
	}
	return token.Close()
}

func (session *ManagedRsyncPointReadSession) begin() error {
	if session == nil || session.adapter == nil || session.token == nil {
		return fmt.Errorf("%w: managed Rsync point reader is unavailable", backupasset.ErrInvalidState)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return fmt.Errorf("%w: managed Rsync point reader session is closed", backupasset.ErrForbidden)
	}
	session.active++
	return nil
}

func (session *ManagedRsyncPointReadSession) end() {
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.active > 0 {
		session.active--
	}
	token := session.releaseTokenLocked()
	session.mu.Unlock()
	if token != nil {
		_ = token.Close()
	}
}

func (session *ManagedRsyncPointReadSession) releaseTokenLocked() publication.AdmissionToken {
	if !session.closed || session.active != 0 || session.tokenReleased || session.token == nil {
		return nil
	}
	session.tokenReleased = true
	return session.token
}

type managedRsyncPointReadHandle struct {
	underlying provider.ReadHandle
	session    *ManagedRsyncPointReadSession
	once       sync.Once
}

func (handle *managedRsyncPointReadHandle) Read(buffer []byte) (int, error) {
	return handle.underlying.Read(buffer)
}

func (handle *managedRsyncPointReadHandle) Close() error {
	if handle == nil || handle.underlying == nil {
		return nil
	}
	err := handle.underlying.Close()
	handle.once.Do(func() { handle.session.end() })
	return err
}

func (handle *managedRsyncPointReadHandle) ProviderBytes() int64 {
	if handle == nil {
		return -1
	}
	reporter, ok := handle.underlying.(provider.ProviderByteReporter)
	if !ok {
		return -1
	}
	return reporter.ProviderBytes()
}
