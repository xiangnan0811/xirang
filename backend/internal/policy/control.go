package policy

import (
	"context"
	"fmt"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ControlService owns policy controls that affect both the policy aggregate and
// its generated tasks. All durable state is committed before scheduler calls.
type ControlService struct {
	db     *gorm.DB
	runner TaskRunner
}

func NewControlService(db *gorm.DB, runner TaskRunner) *ControlService {
	return &ControlService{db: db, runner: runner}
}

// PauseNext marks every currently applicable generated task, not just the
// policy row. Task execution rechecks this flag under the task-row lock.
func (s *ControlService) PauseNext(ctx context.Context, policyID uint) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("policy control unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.PauseNextTx(ctx, tx, policyID)
	})
}

// PauseNextTx applies the durable policy/task control inside the caller's
// transaction. It intentionally does not touch the process-local scheduler;
// callers reconcile schedules only after the outermost commit.
func (s *ControlService) PauseNextTx(ctx context.Context, tx *gorm.DB, policyID uint) error {
	if s == nil || tx == nil {
		return fmt.Errorf("policy control unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx = tx.WithContext(ctx)
	var policy model.Policy
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", policyID).Limit(1).Find(&policy)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("policy %d not found", policyID)
	}
	if err := tx.Model(&model.Policy{}).Where("id = ?", policyID).Update("skip_next", true).Error; err != nil {
		return err
	}
	return tx.Model(&model.Task{}).
		Where("policy_id = ? AND source = ? AND enabled = ? AND cron_spec <> ?", policyID, "policy", true, "").
		Update("skip_next", true).Error
}

// Disable turns off policy scheduling in the database and removes scheduler
// entries only after commit. It deliberately preserves generated Task.Enabled
// so a later policy enable can restore the cron specification.
func (s *ControlService) Disable(ctx context.Context, policyID uint) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("policy control unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var taskIDs []uint
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		taskIDs, err = s.DisableTx(ctx, tx, policyID)
		return err
	}); err != nil {
		return err
	}
	return RemovePolicySchedules(s.db, s.runner, taskIDs)
}

// DisableTx applies the durable policy/task control inside the caller's
// transaction and returns the affected task IDs for post-commit schedule
// reconciliation.
func (s *ControlService) DisableTx(ctx context.Context, tx *gorm.DB, policyID uint) ([]uint, error) {
	if s == nil || tx == nil {
		return nil, fmt.Errorf("policy control unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx = tx.WithContext(ctx)
	var policy model.Policy
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", policyID).Limit(1).Find(&policy)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("policy %d not found", policyID)
	}
	var taskIDs []uint
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Pluck("id", &taskIDs).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Policy{}).Where("id = ?", policyID).Update("enabled", false).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&model.Task{}).Where("policy_id = ? AND source = ?", policyID, "policy").Update("cron_spec", "").Error; err != nil {
		return nil, err
	}
	return taskIDs, nil
}
