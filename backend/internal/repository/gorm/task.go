package gorm

import (
	"context"

	"gorm.io/gorm"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
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

// Update saves changes to an existing task.
func (r *TaskRepository) Update(ctx context.Context, task *model.Task) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Save(task).Error)
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
