package repository

import (
	"context"
	"errors"

	"xirang/backend/internal/model"
)

// ErrNodeSSHKeyNotFound reports a key removed before the node write acquired it.
var ErrNodeSSHKeyNotFound = errors.New("node SSH key reference not found")

// ErrNodeDrillTargetInUse reports a node targeted by an enabled drill.
var ErrNodeDrillTargetInUse = errors.New("node is an enabled drill target")

// ErrNodeRevisionConflict reports that the node changed since it was read.
var ErrNodeRevisionConflict = errors.New("node revision conflict")

// NodeUpdateOptions controls which optional node fields are written.
type NodeUpdateOptions struct {
	UpdateStatus     bool
	UpdateBasePath   bool
	ExpectedRevision *string
	// Merge runs against the locked row after revision comparison and before
	// the referenced key is pinned. It must not perform database operations.
	Merge      func(*model.Node) error
	ApplyTimes func(*model.Node) error
}

// NodeRepository defines the data access interface for Node.
type NodeRepository interface {
	FindByID(ctx context.Context, id uint) (*model.Node, error)
	FindByIDWithSSHKey(ctx context.Context, id uint) (*model.Node, error)
	FindByHost(ctx context.Context, host string) (*model.Node, error)
	List(ctx context.Context) ([]model.Node, error)
	Create(ctx context.Context, node *model.Node) error
	Update(ctx context.Context, node *model.Node, options NodeUpdateOptions) error
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
