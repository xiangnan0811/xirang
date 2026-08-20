package repository

import (
	"context"
	"errors"

	"xirang/backend/internal/model"
)

// ErrTaskArchived is returned when a write targets a Task whose archived_at is set.
var ErrTaskArchived = errors.New("task archived")

// TaskRepository defines the data access interface for Task.
type TaskRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Task, error)
	FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error)
	List(ctx context.Context) ([]model.Task, error)
	Create(ctx context.Context, task *model.Task) error
	Update(ctx context.Context, task *model.Task) error
	Delete(ctx context.Context, id uint) error
	ExistsByID(ctx context.Context, id uint) (bool, error)
	ExistsLiveByID(ctx context.Context, id uint) (bool, error)
	CountByID(ctx context.Context, id uint) (int64, error)
	LockIDsForUpdate(ctx context.Context, ids []uint) error
	RunInTransaction(ctx context.Context, fn func(ctx context.Context, txRepo TaskRepository) error) error

	// FindByIDsFields returns tasks matching the given IDs, selecting only the
	// specified fields.
	FindByIDsFields(ctx context.Context, ids []uint, fields ...string) ([]model.Task, error)
}
