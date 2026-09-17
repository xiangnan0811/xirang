package repository

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/model"
)

// ErrTaskArchived is returned when a write targets a Task whose archived_at is set.
var ErrTaskArchived = errors.New("task archived")

// ErrTaskRevisionConflict is returned when an update was based on an obsolete
// UpdatedAt UnixNano revision.
var ErrTaskRevisionConflict = errors.New("task revision conflict")

// ErrTaskNodeNotFound is returned when a task references a missing node.
var ErrTaskNodeNotFound = errors.New("task node reference not found")

// ErrTaskPolicyNotFound is returned when a task references a missing policy.
var ErrTaskPolicyNotFound = errors.New("task policy reference not found")

// TaskRepository defines the data access interface for Task.
type TaskRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Task, error)
	FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error)
	List(ctx context.Context) ([]model.Task, error)
	Create(ctx context.Context, task *model.Task) error
	Update(ctx context.Context, task *model.Task) error
	// UpdateWithRevision updates one live task only when its durable
	// UpdatedAt timestamp exactly matches expected. The implementation must
	// preserve model hooks (including ExecutorConfig encryption).
	UpdateWithRevision(ctx context.Context, task *model.Task, expected time.Time) error
	Delete(ctx context.Context, id uint) error
	ExistsByID(ctx context.Context, id uint) (bool, error)
	ExistsLiveByID(ctx context.Context, id uint) (bool, error)
	CountByID(ctx context.Context, id uint) (int64, error)

	// LockTaskUpdateReferences acquires referenced policy, task, and node rows
	// in the repository-wide policy -> task -> node order. Missing references
	// return ErrTaskPolicyNotFound/ErrTaskNodeNotFound.
	LockTaskUpdateReferences(ctx context.Context, taskID, nodeID uint, policyID *uint) error

	LockIDsForUpdate(ctx context.Context, ids []uint) error

	// LockTargetOwnership serializes all local target ownership decisions for
	// the duration of the surrounding transaction, including absent targets.
	LockTargetOwnership(ctx context.Context) error
	RunInTransaction(ctx context.Context, fn func(ctx context.Context, txRepo TaskRepository) error) error

	// FindByIDsFields returns tasks matching the given IDs, selecting only the
	// specified fields.
	FindByIDsFields(ctx context.Context, ids []uint, fields ...string) ([]model.Task, error)
	// TaskRetryCronCursorMode reads the newest ordinary retry effect's
	// provenance. The caller must use the transaction-scoped repository after
	// locking the Task row when making a schedule decision.
	TaskRetryCronCursorMode(ctx context.Context, taskID uint) (model.TaskRunCronCursorMode, error)
}
