package repository

import (
	"context"

	"xirang/backend/internal/model"
)

// PolicyRepository defines the data access interface for Policy.
type PolicyRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Policy, error)
	FindByIDWithNodes(ctx context.Context, id uint) (*model.Policy, error)
	List(ctx context.Context) ([]model.Policy, error)
	Create(ctx context.Context, policy *model.Policy) error
	Update(ctx context.Context, policy *model.Policy) error
	Delete(ctx context.Context, id uint) error
	ExistsByID(ctx context.Context, id uint) (bool, error)
	CountByID(ctx context.Context, id uint) (int64, error)

	// CreatePolicyNode creates a policy-node association.
	CreatePolicyNode(ctx context.Context, pn *model.PolicyNode) error

	// Transaction executes fn within a database transaction. The fn receives
	// a new PolicyRepository that operates within the transaction.
	Transaction(ctx context.Context, fn func(repo PolicyRepository) error) error
}
