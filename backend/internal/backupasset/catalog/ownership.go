package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

const maxOwnershipCandidateIDs = 2000

type AuthorizationScope struct {
	Role   string
	UserID uint
}

type Ownership struct {
	db *gorm.DB
}

type pointOwnershipControl struct {
	ID                 string
	RepositoryID       string
	ProducingTaskID    *uint
	ProducingTaskRunID *uint
	Semantics          string
	State              string
	LineageJSON        string
}

type taskRunOwnershipControl struct {
	ID     uint
	TaskID uint
}

type linkOwnershipControl struct {
	ID           string
	TaskID       *uint
	RepositoryID string
	UnlinkedAt   *time.Time
}

func NewOwnership(db *gorm.DB) (*Ownership, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: Catalog ownership database unavailable", backupasset.ErrInvalidState)
	}
	return &Ownership{db: db}, nil
}

func ValidateAuthorizationScope(scope AuthorizationScope) error {
	if scope.UserID == 0 || (scope.Role != "admin" && scope.Role != "operator") {
		return fmt.Errorf("%w: Catalog authorization scope", backupasset.ErrForbidden)
	}
	return nil
}

// AuthorizedPointIDs performs control-plane authorization before callers load
// point names, Catalog counts, evidence, or any other display projection.
func (ownership *Ownership) AuthorizedPointIDs(
	ctx context.Context,
	scope AuthorizationScope,
	candidateIDs []string,
) ([]string, error) {
	if ownership == nil || ownership.db == nil {
		return nil, fmt.Errorf("%w: Catalog ownership unavailable", backupasset.ErrInvalidState)
	}
	if err := ValidateAuthorizationScope(scope); err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return []string{}, nil
	}
	if len(candidateIDs) > maxOwnershipCandidateIDs {
		return nil, fmt.Errorf("%w: Catalog ownership candidate limit", ErrOwnershipProjectionLimit)
	}
	unique := make([]string, 0, len(candidateIDs))
	seen := make(map[string]struct{}, len(candidateIDs))
	for _, id := range candidateIDs {
		if backupasset.ValidateOpaqueID(id) != nil {
			return nil, fmt.Errorf("%w: invalid Catalog ownership candidate", ErrInvalidAssetReference)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	query := ownership.db.WithContext(ctx).Table("recovery_points AS points").
		Select(`points.id, points.repository_id, points.producing_task_id, points.producing_task_run_id,
			points.semantics, points.state, points.lineage_json`).
		Where("points.id IN ?", unique)
	if scope.Role == "operator" {
		query = query.
			Joins("JOIN tasks AS producing_tasks ON producing_tasks.id = points.producing_task_id AND producing_tasks.archived_at IS NULL").
			Joins("JOIN node_owners AS producing_owners ON producing_owners.node_id = producing_tasks.node_id AND producing_owners.user_id = ?", scope.UserID).
			Where("points.producing_task_id IS NOT NULL AND points.semantics <> ?", backupasset.PointImportedBaseline)
	}
	var controls []pointOwnershipControl
	if err := query.Scan(&controls).Error; err != nil {
		return nil, fmt.Errorf("load Catalog ownership controls: %w", err)
	}
	byID := make(map[string]pointOwnershipControl, len(controls))
	for _, control := range controls {
		byID[control.ID] = control
	}
	if scope.Role == "admin" {
		return orderedAuthorizedIDs(unique, byID, nil), nil
	}
	authorized, err := ownership.validateOperatorLineages(ctx, controls)
	if err != nil {
		return nil, err
	}
	return orderedAuthorizedIDs(unique, byID, authorized), nil
}

func (ownership *Ownership) validateOperatorLineages(
	ctx context.Context,
	controls []pointOwnershipControl,
) (map[string]struct{}, error) {
	authorized := make(map[string]struct{}, len(controls))
	immutableLineages := make(map[string]backupasset.PublicationLineageV1, len(controls))
	runIDs := make([]uint, 0, len(controls))
	linkIDs := make([]string, 0, len(controls))
	mutableTasks := make([]uint, 0, len(controls))
	mutableCandidates := make(map[string]pointOwnershipControl)

	for _, control := range controls {
		if control.ProducingTaskID == nil {
			continue
		}
		switch backupasset.PointVersionSemantics(control.Semantics) {
		case backupasset.PointMutableHead:
			if control.State != string(backupasset.RecoveryPointObserved) ||
				!validMutableOwnershipLineage(control.LineageJSON, *control.ProducingTaskID) {
				continue
			}
			mutableTasks = append(mutableTasks, *control.ProducingTaskID)
			mutableCandidates[control.ID] = control
		case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest:
			if control.ProducingTaskRunID == nil {
				continue
			}
			lineage, err := backupasset.DecodePublicationLineage(control.LineageJSON)
			if err != nil || lineage.TaskID != *control.ProducingTaskID || lineage.TaskRunID != *control.ProducingTaskRunID {
				continue
			}
			immutableLineages[control.ID] = lineage
			runIDs = append(runIDs, lineage.TaskRunID)
			linkIDs = append(linkIDs, lineage.TaskRepositoryLinkID)
		}
	}

	runs := make(map[uint]taskRunOwnershipControl, len(runIDs))
	if len(runIDs) > 0 {
		var rows []taskRunOwnershipControl
		if err := ownership.db.WithContext(ctx).Table("task_runs").Select("id", "task_id").Where("id IN ?", runIDs).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load Catalog producing runs: %w", err)
		}
		for _, row := range rows {
			runs[row.ID] = row
		}
	}
	links := make(map[string]linkOwnershipControl, len(linkIDs))
	if len(linkIDs) > 0 {
		var rows []linkOwnershipControl
		if err := ownership.db.WithContext(ctx).Table("task_repository_links").
			Select("id", "task_id", "repository_id", "unlinked_at").Where("id IN ?", linkIDs).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load Catalog producing links: %w", err)
		}
		for _, row := range rows {
			links[row.ID] = row
		}
	}
	for pointID, lineage := range immutableLineages {
		control := pointOwnershipControl{}
		for _, candidate := range controls {
			if candidate.ID == pointID {
				control = candidate
				break
			}
		}
		run, runExists := runs[lineage.TaskRunID]
		link, linkExists := links[lineage.TaskRepositoryLinkID]
		if !runExists || run.TaskID != lineage.TaskID || !linkExists || link.TaskID == nil || *link.TaskID != lineage.TaskID ||
			link.RepositoryID != control.RepositoryID {
			continue
		}
		authorized[pointID] = struct{}{}
	}

	if len(mutableTasks) > 0 {
		var activeLinks []linkOwnershipControl
		if err := ownership.db.WithContext(ctx).Table("task_repository_links").
			Select("id", "task_id", "repository_id", "unlinked_at").
			Where("task_id IN ? AND unlinked_at IS NULL", mutableTasks).Scan(&activeLinks).Error; err != nil {
			return nil, fmt.Errorf("load mutable Catalog producing links: %w", err)
		}
		for pointID, control := range mutableCandidates {
			for _, link := range activeLinks {
				if link.TaskID != nil && *link.TaskID == *control.ProducingTaskID && link.RepositoryID == control.RepositoryID {
					authorized[pointID] = struct{}{}
					break
				}
			}
		}
	}
	return authorized, nil
}

func validMutableOwnershipLineage(raw string, taskID uint) bool {
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

func orderedAuthorizedIDs(
	ordered []string,
	existing map[string]pointOwnershipControl,
	authorized map[string]struct{},
) []string {
	result := make([]string, 0, len(ordered))
	for _, id := range ordered {
		if _, exists := existing[id]; !exists {
			continue
		}
		if authorized != nil {
			if _, allowed := authorized[id]; !allowed {
				continue
			}
		}
		result = append(result, id)
	}
	return result
}
