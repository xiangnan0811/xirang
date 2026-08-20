package retention

import (
	"context"
	"fmt"
	"sort"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ManagedTaskRetentionFacadeDependencies struct {
	DB          *gorm.DB
	Policies    *PolicyService
	Coordinator *Coordinator
	Now         func() time.Time
}

type ManagedTaskRetentionFacade struct {
	db          *gorm.DB
	policies    *PolicyService
	coordinator *Coordinator
	now         func() time.Time
}

func NewManagedTaskRetentionFacade(dependencies ManagedTaskRetentionFacadeDependencies) (*ManagedTaskRetentionFacade, error) {
	if dependencies.DB == nil || dependencies.Policies == nil || dependencies.Coordinator == nil {
		return nil, fmt.Errorf("%w: managed Task retention facade is unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &ManagedTaskRetentionFacade{
		db: dependencies.DB, policies: dependencies.Policies,
		coordinator: dependencies.Coordinator, now: dependencies.Now,
	}, nil
}

func (facade *ManagedTaskRetentionFacade) EnforceManagedRetention(
	ctx context.Context,
	request task.ManagedRecoveryPointRetentionRequest,
) error {
	pointIDs, err := validateManagedTaskRetentionRequest(request)
	if err != nil {
		return err
	}
	err = facade.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentTask model.Task
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "policy_id").Where("id = ?", request.TaskID).Limit(1).Find(&currentTask)
		if loaded.Error != nil {
			return fmt.Errorf("load managed Task retention Task: %w", loaded.Error)
		}
		if loaded.RowsAffected != 1 || currentTask.PolicyID == nil || *currentTask.PolicyID != request.PolicyID {
			return fmt.Errorf("%w: managed Task retention policy binding", backupasset.ErrConflict)
		}

		var linkIdentity model.TaskRepositoryLink
		loaded = tx.WithContext(ctx).Select("id").
			Where("task_id = ? AND repository_id = ? AND unlinked_at IS NULL", request.TaskID, request.RepositoryID).
			Limit(1).Find(&linkIdentity)
		if loaded.Error != nil {
			return fmt.Errorf("load managed Task retention link: %w", loaded.Error)
		}
		if loaded.RowsAffected != 1 {
			return fmt.Errorf("%w: managed Task retention link", backupasset.ErrConflict)
		}
		var policy model.BackupRetentionPolicy
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope_kind = ? AND scope_id = ? AND status = ?",
				backupasset.RetentionPolicyScopeTaskLink, linkIdentity.ID, backupasset.RetentionPolicyActive).
			Limit(1).Find(&policy)
		if loaded.Error != nil {
			return fmt.Errorf("load managed Task retention policy: %w", loaded.Error)
		}
		if loaded.RowsAffected != 1 {
			return fmt.Errorf("%w: managed Task retention policy", backupasset.ErrNotFound)
		}
		var link model.TaskRepositoryLink
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND repository_id = ? AND unlinked_at IS NULL",
				linkIdentity.ID, request.TaskID, request.RepositoryID).
			Limit(1).Find(&link)
		if loaded.Error != nil {
			return fmt.Errorf("lock managed Task retention link: %w", loaded.Error)
		}
		if loaded.RowsAffected != 1 {
			return fmt.Errorf("%w: managed Task retention link changed", backupasset.ErrConflict)
		}
		var exactPoints []model.RecoveryPoint
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ? AND repository_id = ? AND producing_task_id = ?", pointIDs, request.RepositoryID, request.TaskID).
			Order("id ASC").Find(&exactPoints).Error; err != nil {
			return fmt.Errorf("validate managed Task retention points: %w", err)
		}
		if len(exactPoints) != len(pointIDs) {
			return fmt.Errorf("%w: managed Task retention point scope", backupasset.ErrConflict)
		}
		selection, err := facade.policies.SelectWithTx(ctx, tx, SelectionRequest{
			PolicyID: policy.ID, ExpectedRevision: policy.Revision, EvaluatedAt: facade.now().UTC(),
		})
		if err != nil {
			return err
		}
		allowed := make(map[string]struct{}, len(pointIDs))
		for _, pointID := range pointIDs {
			allowed[pointID] = struct{}{}
		}
		for _, selected := range selection.Points {
			if _, exact := allowed[selected.RecoveryPointID]; !exact {
				continue
			}
			if _, err := facade.coordinator.ClaimTx(ctx, tx, ClaimRequest{
				RecoveryPointID: selected.RecoveryPointID,
				Operation:       backupasset.LifecycleRetentionExpire,
				PolicySelection: &selection,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func validateManagedTaskRetentionRequest(request task.ManagedRecoveryPointRetentionRequest) ([]string, error) {
	if request.TaskID == 0 || request.PolicyID == 0 || backupasset.ValidateOpaqueID(request.RepositoryID) != nil ||
		len(request.RecoveryPointIDs) == 0 || len(request.RecoveryPointIDs) > 10000 {
		return nil, fmt.Errorf("%w: invalid managed Task retention request", backupasset.ErrInvalidState)
	}
	pointIDs := append([]string(nil), request.RecoveryPointIDs...)
	sort.Strings(pointIDs)
	for index, pointID := range pointIDs {
		if backupasset.ValidateOpaqueID(pointID) != nil || index > 0 && pointID == pointIDs[index-1] {
			return nil, fmt.Errorf("%w: invalid managed Task retention point identity", backupasset.ErrInvalidState)
		}
	}
	return pointIDs, nil
}

var _ task.ManagedRecoveryPointRetention = (*ManagedTaskRetentionFacade)(nil)
