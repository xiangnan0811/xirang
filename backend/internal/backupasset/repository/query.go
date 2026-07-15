package repository

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
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
	AccessActive bool             `json:"access_active"`
	Lineages     []LineageSummary `json:"lineages"`
}

type LineageSummary struct {
	Source             string `json:"source"`
	TaskID             *uint  `json:"task_id,omitempty"`
	TaskName           string `json:"task_name"`
	NodeID             uint   `json:"node_id"`
	NodeName           string `json:"node_name"`
	PublicationMode    string `json:"publication_mode,omitempty"`
	RecoveryPointID    string `json:"recovery_point_id,omitempty"`
	RecoveryPointState string `json:"recovery_point_state,omitempty"`
	PointSemantics     string `json:"point_semantics,omitempty"`
	Active             bool   `json:"active"`
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
	query := service.db.WithContext(ctx).Model(&model.BackupRepository{})
	if scope.Role == "operator" {
		query = applyOperatorRepositoryVisibility(query, scope.UserID)
	}
	if strings.TrimSpace(request.Cursor) != "" {
		cursor, err := service.decodeRepositoryCursor(ctx, request.Cursor, scope)
		if err != nil {
			return RepositoryPage{}, err
		}
		query = query.Where(`backup_repositories.created_at < ? OR
			(backup_repositories.created_at = ? AND backup_repositories.id < ?)`, cursor.CreatedAt, cursor.CreatedAt, cursor.RepositoryID)
	}
	var repositories []model.BackupRepository
	if err := query.Order("backup_repositories.created_at DESC, backup_repositories.id DESC").Limit(limit + 1).Find(&repositories).Error; err != nil {
		return RepositoryPage{}, fmt.Errorf("list visible backup repositories: %w", err)
	}
	hasMore := len(repositories) > limit
	if hasMore {
		repositories = repositories[:limit]
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
	query := service.db.WithContext(ctx).Model(&model.BackupRepository{}).Where("backup_repositories.id = ?", repositoryID)
	if scope.Role == "operator" {
		query = applyOperatorRepositoryVisibility(query, scope.UserID)
	}
	var repository model.BackupRepository
	if err := query.First(&repository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
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

func applyOperatorRepositoryVisibility(query *gorm.DB, userID uint) *gorm.DB {
	return query.Where(`
		EXISTS (
			SELECT 1
			FROM task_repository_links AS visible_links
			JOIN tasks AS visible_link_tasks ON visible_link_tasks.id = visible_links.task_id
			JOIN node_owners AS visible_link_owners ON visible_link_owners.node_id = visible_link_tasks.node_id AND visible_link_owners.user_id = ?
			WHERE visible_links.repository_id = backup_repositories.id
			  AND visible_links.task_id IS NOT NULL
			  AND visible_links.unlinked_at IS NULL
			  AND visible_link_tasks.archived_at IS NULL
		)
		OR EXISTS (
			SELECT 1
			FROM recovery_points AS visible_points
			JOIN tasks AS visible_point_tasks ON visible_point_tasks.id = visible_points.producing_task_id
			JOIN node_owners AS visible_point_owners ON visible_point_owners.node_id = visible_point_tasks.node_id AND visible_point_owners.user_id = ?
			WHERE visible_points.repository_id = backup_repositories.id
			  AND visible_points.producing_task_id IS NOT NULL
			  AND visible_point_tasks.archived_at IS NULL
		)
	`, userID, userID)
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
	return RepositoryView{RepositoryDTO: dto, AccessActive: activeBindings > 0, Lineages: lineages}, nil
}

func (service *Service) loadLineages(ctx context.Context, repositoryID string, scope VisibilityScope) ([]LineageSummary, error) {
	lineages := make([]LineageSummary, 0)
	linkQuery := service.db.WithContext(ctx).Table("task_repository_links AS links").Where("links.repository_id = ?", repositoryID)
	if scope.Role == "operator" {
		linkQuery = linkQuery.
			Joins("JOIN tasks AS link_tasks ON link_tasks.id = links.task_id AND link_tasks.archived_at IS NULL").
			Joins("JOIN node_owners AS link_owners ON link_owners.node_id = link_tasks.node_id AND link_owners.user_id = ?", scope.UserID).
			Where("links.task_id IS NOT NULL AND links.unlinked_at IS NULL")
	}
	var links []struct {
		TaskID          *uint
		TaskName        string
		NodeID          uint
		NodeName        string
		PublicationMode string
		UnlinkedAt      *time.Time
	}
	if err := linkQuery.Select(`links.task_id, links.task_name_snapshot AS task_name,
		links.node_id_snapshot AS node_id, links.node_name_snapshot AS node_name,
		links.publication_mode, links.unlinked_at`).
		Order("links.created_at ASC, links.id ASC").Scan(&links).Error; err != nil {
		return nil, fmt.Errorf("load repository Task lineages: %w", err)
	}
	for _, link := range links {
		lineages = append(lineages, LineageSummary{
			Source: lineageSourceTaskLink, TaskID: link.TaskID, TaskName: link.TaskName, NodeID: link.NodeID,
			NodeName: link.NodeName, PublicationMode: link.PublicationMode, Active: link.UnlinkedAt == nil,
		})
	}

	pointQuery := service.db.WithContext(ctx).Table("recovery_points AS points").Where("points.repository_id = ?", repositoryID)
	if scope.Role == "operator" {
		pointQuery = pointQuery.
			Joins("JOIN tasks AS point_tasks ON point_tasks.id = points.producing_task_id AND point_tasks.archived_at IS NULL").
			Joins("JOIN node_owners AS point_owners ON point_owners.node_id = point_tasks.node_id AND point_owners.user_id = ?", scope.UserID).
			Where("points.producing_task_id IS NOT NULL")
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

// BeginManagedRsyncPointRead reconstructs one exact committed managed Rsync
// tree. It never accepts a root, path, marker, or locator from its caller.
func (service *Service) BeginManagedRsyncPointRead(ctx context.Context, taskID uint, recoveryPointID string) (*ManagedRsyncPointReadSession, error) {
	if service == nil || service.db == nil || service.foundation == nil || service.admission == nil || service.keyring == nil ||
		taskID == 0 || backupasset.ValidateOpaqueID(recoveryPointID) != nil {
		return nil, fmt.Errorf("%w: managed Rsync point reader request is invalid", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := service.admission.Acquire(ctx, publication.OperationManagedRsyncPointRead)
	if err != nil {
		return nil, err
	}
	keepToken := false
	defer func() {
		if !keepToken {
			_ = token.Close()
		}
	}()
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
	keepToken = true
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
