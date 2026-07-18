package search

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
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"

	"gorm.io/gorm"
)

const ownershipBatchSize = 2000

type PointAuthorizer interface {
	AuthorizedPointIDs(context.Context, catalog.AuthorizationScope, []string) ([]string, error)
}

type ScopeResolverLimits struct {
	MaxCandidates int
}

type ScopeResolver struct {
	db         *gorm.DB
	authorizer PointAuthorizer
	limits     ScopeResolverLimits
}

type SelectedPoint struct {
	ID              string
	RepositoryID    string
	ProducingTaskID uint
	Semantics       backupasset.PointVersionSemantics
	Lineage         string
	CapturedAt      *time.Time
	CommittedAt     *time.Time
	ObservedAt      *time.Time
	CreatedAt       time.Time
	Current         bool
}

type ScopeSelection struct {
	Mode           SearchScopeMode
	Points         []SelectedPoint
	RevisionDigest string
}

type scopePointControl struct {
	ID                 string
	RepositoryID       string
	ProducingTaskID    *uint
	ProducingTaskRunID *uint
	Semantics          string
	State              string
	LineageJSON        string
	CapturedAt         *time.Time
	CommittedAt        *time.Time
	ObservedAt         *time.Time
	CreatedAt          time.Time
}

func NewScopeResolver(db *gorm.DB, authorizer PointAuthorizer, limits ScopeResolverLimits) (*ScopeResolver, error) {
	if db == nil || authorizer == nil || limits.MaxCandidates <= 0 {
		return nil, fmt.Errorf("%w: scope resolver dependencies", ErrInvalidScope)
	}
	return &ScopeResolver{db: db, authorizer: authorizer, limits: limits}, nil
}

func (resolver *ScopeResolver) Resolve(
	ctx context.Context,
	authorization catalog.AuthorizationScope,
	scope SearchScope,
) (ScopeSelection, error) {
	canonical, err := canonicalizeScope(scope)
	if err != nil {
		return ScopeSelection{}, fmt.Errorf("%w: scope schema", ErrInvalidScope)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := resolver.loadCandidateIDs(ctx, canonical)
	if err != nil {
		return ScopeSelection{}, err
	}
	if len(candidates) > resolver.limits.MaxCandidates {
		return ScopeSelection{}, ErrResourceLimit
	}
	if canonical.Mode == SearchScopeExactPoints && len(candidates) != len(canonical.RecoveryPointIDs) {
		return ScopeSelection{}, ErrScopeStale
	}
	visible, err := resolver.authorizeBatches(ctx, authorization, candidates)
	if err != nil {
		return ScopeSelection{}, err
	}
	if canonical.Mode == SearchScopeExactPoints && len(visible) != len(candidates) {
		return ScopeSelection{}, ErrScopeStale
	}
	controls, err := resolver.loadVisibleControls(ctx, visible)
	if err != nil {
		return ScopeSelection{}, err
	}
	points := make([]SelectedPoint, 0, len(controls))
	for _, control := range controls {
		point, err := selectedPointFromControl(control, authorization.Role)
		if err != nil {
			return ScopeSelection{}, err
		}
		points = append(points, point)
	}
	if canonical.Mode == SearchScopeCurrent {
		points = currentRepresentatives(points)
		for index := range points {
			points[index].Current = true
		}
	}
	digest := scopeSelectionDigest(canonical.Mode, points)
	return ScopeSelection{Mode: canonical.Mode, Points: points, RevisionDigest: digest}, nil
}

func (resolver *ScopeResolver) loadCandidateIDs(ctx context.Context, scope SearchScope) ([]string, error) {
	query := resolver.db.WithContext(ctx).Table("recovery_points").Select("id")
	switch scope.Mode {
	case SearchScopeCurrent:
		query = query.Where(`(semantics = ? AND state = ?) OR (semantics IN ? AND state IN ?)`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest)},
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)})
	case SearchScopeAllRetained:
		query = query.Where(`(semantics = ? AND state = ?) OR (semantics IN ? AND state IN ?)`,
			backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
			[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)},
			[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)})
	case SearchScopeExactPoints:
		query = query.Where("id IN ?", scope.RecoveryPointIDs).
			Where(`(semantics = ? AND state = ?) OR (semantics IN ? AND state IN ?)`,
				backupasset.PointMutableHead, backupasset.RecoveryPointObserved,
				[]string{string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline)},
				[]string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)})
	default:
		return nil, ErrInvalidScope
	}
	if len(scope.RepositoryIDs) > 0 {
		query = query.Where("repository_id IN ?", scope.RepositoryIDs)
	}
	if len(scope.TaskIDs) > 0 {
		query = query.Where("producing_task_id IN ?", scope.TaskIDs)
	}
	var ids []string
	if err := query.Order("id ASC").Limit(resolver.limits.MaxCandidates + 1).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("load search scope candidates: %w", err)
	}
	return ids, nil
}

func (resolver *ScopeResolver) authorizeBatches(
	ctx context.Context,
	authorization catalog.AuthorizationScope,
	candidates []string,
) ([]string, error) {
	visible := make([]string, 0, len(candidates))
	for start := 0; start < len(candidates); start += ownershipBatchSize {
		end := min(start+ownershipBatchSize, len(candidates))
		batch, err := resolver.authorizer.AuthorizedPointIDs(ctx, authorization, candidates[start:end])
		if err != nil {
			return nil, err
		}
		visible = append(visible, batch...)
	}
	return visible, nil
}

func (resolver *ScopeResolver) loadVisibleControls(ctx context.Context, ids []string) ([]scopePointControl, error) {
	if len(ids) == 0 {
		return []scopePointControl{}, nil
	}
	var rows []scopePointControl
	if err := resolver.db.WithContext(ctx).Table("recovery_points").
		Select(`id, repository_id, producing_task_id, producing_task_run_id, semantics, state,
			lineage_json, captured_at, committed_at, observed_at, created_at`).
		Where("id IN ?", ids).Order("id ASC").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load authorized search scope controls: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, ErrScopeStale
	}
	return rows, nil
}

func selectedPointFromControl(control scopePointControl, role string) (SelectedPoint, error) {
	if backupasset.ValidateOpaqueID(control.ID) != nil || backupasset.ValidateOpaqueID(control.RepositoryID) != nil || control.CreatedAt.IsZero() {
		return SelectedPoint{}, ErrScopeStale
	}
	point := SelectedPoint{
		ID: control.ID, RepositoryID: control.RepositoryID, Semantics: backupasset.PointVersionSemantics(control.Semantics),
		CapturedAt: utcPointer(control.CapturedAt), CommittedAt: utcPointer(control.CommittedAt),
		ObservedAt: utcPointer(control.ObservedAt), CreatedAt: control.CreatedAt.UTC(),
	}
	switch point.Semantics {
	case backupasset.PointImportedBaseline:
		if role != "admin" {
			return SelectedPoint{}, ErrScopeStale
		}
		point.Lineage = "imported:" + point.ID
	case backupasset.PointMutableHead:
		if control.ProducingTaskID == nil || control.ProducingTaskRunID != nil ||
			control.State != string(backupasset.RecoveryPointObserved) || !validMutableScopeLineage(control.LineageJSON, *control.ProducingTaskID) {
			return SelectedPoint{}, ErrScopeStale
		}
		point.ProducingTaskID = *control.ProducingTaskID
		point.Lineage = "mutable:" + strconv.FormatUint(uint64(*control.ProducingTaskID), 10) + ":" + control.RepositoryID
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest:
		if control.ProducingTaskID == nil || control.ProducingTaskRunID == nil {
			return SelectedPoint{}, ErrScopeStale
		}
		lineage, err := backupasset.DecodePublicationLineage(control.LineageJSON)
		if err != nil || lineage.TaskID != *control.ProducingTaskID || lineage.TaskRunID != *control.ProducingTaskRunID {
			return SelectedPoint{}, ErrScopeStale
		}
		point.ProducingTaskID = *control.ProducingTaskID
		point.Lineage = "publication:" + strconv.FormatUint(uint64(lineage.TaskID), 10) + ":" + lineage.TaskRepositoryLinkID
	default:
		return SelectedPoint{}, ErrScopeStale
	}
	return point, nil
}

func validMutableScopeLineage(raw string, taskID uint) bool {
	var lineage backupasset.RecoveryPointLineageSummary
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lineage); err != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false
	}
	return lineage.ProducingTaskID != nil && *lineage.ProducingTaskID == taskID &&
		lineage.ProducingTaskRunID == nil && lineage.SourcePointID == ""
}

func currentRepresentatives(points []SelectedPoint) []SelectedPoint {
	result := make([]SelectedPoint, 0, len(points))
	positions := make(map[string]int, len(points))
	for _, point := range points {
		position, exists := positions[point.Lineage]
		if !exists {
			positions[point.Lineage] = len(result)
			result = append(result, point)
			continue
		}
		if newerScopePoint(point, result[position]) {
			result[position] = point
		}
	}
	return result
}

func newerScopePoint(left, right SelectedPoint) bool {
	for _, pair := range [][2]*time.Time{{left.CommittedAt, right.CommittedAt}, {left.CapturedAt, right.CapturedAt}} {
		if pair[0] != nil || pair[1] != nil {
			if pair[0] == nil {
				return false
			}
			if pair[1] == nil {
				return true
			}
			if !pair[0].Equal(*pair[1]) {
				return pair[0].After(*pair[1])
			}
		}
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID > right.ID
}

func scopeSelectionDigest(mode SearchScopeMode, points []SelectedPoint) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("xirang/search/scope/v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(mode))
	for _, point := range points {
		for _, value := range []string{point.ID, point.RepositoryID, point.Lineage, point.CreatedAt.UTC().Format(time.RFC3339Nano)} {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(value))
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
