package repository

import (
	"context"

	"xirang/backend/internal/model"
)

// NodeRepository defines the data access interface for Node.
type NodeRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Node, error)
	FindByIDWithSSHKey(ctx context.Context, id uint) (*model.Node, error)
	FindByHost(ctx context.Context, host string) (*model.Node, error)
	List(ctx context.Context) ([]model.Node, error)
	Create(ctx context.Context, node *model.Node) error
	Update(ctx context.Context, node *model.Node) error
	Delete(ctx context.Context, id uint) error
	ExistsByID(ctx context.Context, id uint) (bool, error)

	// DeleteWithAssociations deletes a node and its associated PolicyNode,
	// Task, and Alert records in a single transaction.
	DeleteWithAssociations(ctx context.Context, id uint) error

	// BatchDeleteWithAssociations deletes multiple nodes and their associated
	// records. Returns the number of deleted nodes and IDs that were not found.
	BatchDeleteWithAssociations(ctx context.Context, ids []uint) (int64, []uint, error)

	// FindSSHKeyByID looks up an SSH key by its primary key.
	FindSSHKeyByID(ctx context.Context, id uint) (*model.SSHKey, error)
}
