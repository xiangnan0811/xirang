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
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	defaultRepositoryPageLimit = 100
	maxRepositoryPageLimit     = 200
	lineageSourceTaskLink      = "task_link"
	lineageSourceRecoveryPoint = "recovery_point"
	repositoryCursorVersion    = 1
	repositoryCursorTTL        = 15 * time.Minute
	repositoryCursorMaxBytes   = 4096
	repositoryCursorDomain     = "backup-repository-list-cursor:v1"
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
