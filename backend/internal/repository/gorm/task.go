package gorm

import (
	"context"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
	"xirang/backend/internal/repository"
)

var _ repository.TaskRepository = (*TaskRepository)(nil)

// TaskRepository implements repository.TaskRepository using GORM.
type TaskRepository struct {
	db *gorm.DB
}

// NewTaskRepository creates a new TaskRepository.
func NewTaskRepository(db *gorm.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

// FindByID returns a task by its primary key.
func (r *TaskRepository) FindByID(ctx context.Context, id uint) (*model.Task, error) {
	var task model.Task
	if err := r.db.WithContext(ctx).First(&task, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &task, nil
}

// FindByIDFields returns a task by its primary key, selecting only the
// specified fields.
func (r *TaskRepository) FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error) {
	var task model.Task
	q := r.db.WithContext(ctx)
	if len(fields) > 0 {
		q = q.Select(fields)
	}
	if err := q.First(&task, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &task, nil
}

// List returns all tasks.
func (r *TaskRepository) List(ctx context.Context) ([]model.Task, error) {
	var tasks []model.Task
	if err := r.db.WithContext(ctx).Find(&tasks).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return tasks, nil
}

// Create inserts a new task.
func (r *TaskRepository) Create(ctx context.Context, task *model.Task) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Create(task).Error)
}

// Update saves changes to an existing unarchived task. A stale Save must not
// clear archived_at after Archive commits.
func (r *TaskRepository) Update(ctx context.Context, task *model.Task) error {
	result := r.db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND archived_at IS NULL", task.ID).
		Select("*").
		Omit("ID", "CreatedAt", "ArchivedAt").
		Updates(task)
	if result.Error != nil {
		return apperr.WrapDBError(result.Error)
	}
	if result.RowsAffected == 0 {
		return repository.ErrTaskArchived
	}
	return nil
}

// UpdateWithRevision performs the task edit compare-and-set. The timestamp
// predicate is evaluated by the database in the same transaction as the
// update, so two writers holding the same stale revision cannot overwrite
// one another. GORM's update callbacks still run to encrypt ExecutorConfig
// and advance UpdatedAt.
func (r *TaskRepository) UpdateWithRevision(ctx context.Context, task *model.Task, expected time.Time) error {
	if r == nil || r.db == nil || task == nil {
		return apperr.WrapDBError(gorm.ErrInvalidData)
	}
	result := r.db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND archived_at IS NULL AND updated_at = ?", task.ID, expected).
		Select("*").
		Omit("ID", "CreatedAt", "ArchivedAt").
		Updates(task)
	if result.Error != nil {
		return apperr.WrapDBError(result.Error)
	}
	if result.RowsAffected == 0 {
		return repository.ErrTaskRevisionConflict
	}
	return nil
}

// Delete removes a task by its primary key.
func (r *TaskRepository) Delete(ctx context.Context, id uint) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Delete(&model.Task{}, id).Error)
}

// ExistsByID returns true if a task with the given id exists.
func (r *TaskRepository) ExistsByID(ctx context.Context, id uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, apperr.WrapDBError(err)
	}
	return count > 0, nil
}

// LockTaskUpdateReferences acquires referenced rows in the repository-wide
// policy -> task -> node order. Existing policy and node rows remain locked
// through the task update, closing the validation-to-write deletion race.
func (r *TaskRepository) LockTaskUpdateReferences(ctx context.Context, taskID, nodeID uint, policyID *uint) error {
	if r == nil || r.db == nil {
		return apperr.WrapDBError(gorm.ErrInvalidDB)
	}
	if policyID != nil {
		var policy model.Policy
		err := r.db.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&policy, *policyID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repository.ErrTaskPolicyNotFound
		}
		if err != nil {
			return apperr.WrapDBError(err)
		}
	}
	if err := r.LockIDsForUpdate(ctx, []uint{taskID}); err != nil {
		return err
	}
	if nodeID != 0 {
		var node model.Node
		err := r.db.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&node, nodeID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repository.ErrTaskNodeNotFound
		}
		if err != nil {
			return apperr.WrapDBError(err)
		}
	}
	return nil
}

// ExistsLiveByID returns true if an unarchived task with the given id exists.
func (r *TaskRepository) ExistsLiveByID(ctx context.Context, id uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Task{}).
		Where("id = ? AND archived_at IS NULL", id).Count(&count).Error; err != nil {
		return false, apperr.WrapDBError(err)
	}
	return count > 0, nil
}

func (r *TaskRepository) LockIDsForUpdate(ctx context.Context, ids []uint) error {
	return LockTaskIDsForUpdate(r.db.WithContext(ctx), ids)
}

func (r *TaskRepository) LockTargetOwnership(ctx context.Context) error {
	if r == nil || r.db == nil {
		return apperr.WrapDBError(gorm.ErrInvalidDB)
	}
	return policy.LockTargetOwnershipSpace(r.db.WithContext(ctx))
}

func (r *TaskRepository) RunInTransaction(ctx context.Context, fn func(ctx context.Context, txRepo repository.TaskRepository) error) error {
	if r == nil || r.db == nil {
		return apperr.WrapDBError(gorm.ErrInvalidDB)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(ctx, NewTaskRepository(tx))
	})
}

// LockTaskIDsForUpdate locks task rows in ascending ID order. SQLite treats
// FOR UPDATE as a no-op, so the same-row name assignment serializes writers.
func LockTaskIDsForUpdate(tx *gorm.DB, ids []uint) error {
	if tx == nil || len(ids) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(ids))
	sorted := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, id := range sorted {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Model(&model.Task{}).
			Where("id = ?", id).
			UpdateColumn("name", gorm.Expr("name"))
		if result.Error != nil {
			return apperr.WrapDBError(result.Error)
		}
	}
	return nil
}

// CountByID returns the count of tasks matching the id.
func (r *TaskRepository) CountByID(ctx context.Context, id uint) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Task{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return 0, apperr.WrapDBError(err)
	}
	return count, nil
}

// FindByIDsFields returns tasks matching the given IDs, selecting only the
// specified fields.
func (r *TaskRepository) FindByIDsFields(ctx context.Context, ids []uint, fields ...string) ([]model.Task, error) {
	var tasks []model.Task
	q := r.db.WithContext(ctx).Where("id IN ?", ids)
	if len(fields) > 0 {
		q = q.Select(fields)
	}
	if err := q.Find(&tasks).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return tasks, nil
}

// TaskRetryCronCursorMode returns the provenance of the newest ordinary retry
// effect for taskID. The underlying handle may be a transaction; callers make
// schedule decisions only after locking the Task row on that same handle.
func (r *TaskRepository) TaskRetryCronCursorMode(ctx context.Context, taskID uint) (model.TaskRunCronCursorMode, error) {
	if r == nil || r.db == nil {
		return model.TaskRunCronCursorModeLegacy, apperr.WrapDBError(gorm.ErrInvalidDB)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return model.LatestTaskRetryEffectCronCursorModeTx(r.db.WithContext(ctx), taskID)
}
