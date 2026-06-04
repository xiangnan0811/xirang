package repository

import (
	"context"

	"xirang/backend/internal/model"
)

// TaskRepository defines the data access interface for Task.
type TaskRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Task, error)
	FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error)
	List(ctx context.Context) ([]model.Task, error)
	Create(ctx context.Context, task *model.Task) error
	Update(ctx context.Context, task *model.Task) error
	Delete(ctx context.Context, id uint) error
	ExistsByID(ctx context.Context, id uint) (bool, error)
	CountByID(ctx context.Context, id uint) (int64, error)

	// FindByIDsFields returns tasks matching the given IDs, selecting only the
	// specified fields.
	FindByIDsFields(ctx context.Context, ids []uint, fields ...string) ([]model.Task, error)
}
