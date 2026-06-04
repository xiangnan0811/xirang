package gorm

import (
	"context"

	"gorm.io/gorm"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
)

var _ repository.NodeRepository = (*NodeRepository)(nil)

// NodeRepository implements repository.NodeRepository using GORM.
type NodeRepository struct {
	db *gorm.DB
}

// NewNodeRepository creates a new NodeRepository.
func NewNodeRepository(db *gorm.DB) *NodeRepository {
	return &NodeRepository{db: db}
}

// FindByID returns a node by its primary key.
func (r *NodeRepository) FindByID(ctx context.Context, id uint) (*model.Node, error) {
	var node model.Node
	if err := r.db.WithContext(ctx).First(&node, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &node, nil
}

// FindByIDWithSSHKey returns a node by its primary key with SSHKey preloaded.
func (r *NodeRepository) FindByIDWithSSHKey(ctx context.Context, id uint) (*model.Node, error) {
	var node model.Node
	if err := r.db.WithContext(ctx).Preload("SSHKey").First(&node, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &node, nil
}

// FindByHost returns a node by its host field.
func (r *NodeRepository) FindByHost(ctx context.Context, host string) (*model.Node, error) {
	var node model.Node
	if err := r.db.WithContext(ctx).Where("host = ?", host).First(&node).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &node, nil
}

// List returns all nodes.
func (r *NodeRepository) List(ctx context.Context) ([]model.Node, error) {
	var nodes []model.Node
	if err := r.db.WithContext(ctx).Find(&nodes).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return nodes, nil
}

// Create inserts a new node.
func (r *NodeRepository) Create(ctx context.Context, node *model.Node) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Create(node).Error)
}

// Update saves changes to an existing node.
func (r *NodeRepository) Update(ctx context.Context, node *model.Node) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Save(node).Error)
}

// Delete removes a node by its primary key.
func (r *NodeRepository) Delete(ctx context.Context, id uint) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Delete(&model.Node{}, id).Error)
}

// ExistsByID returns true if a node with the given id exists.
func (r *NodeRepository) ExistsByID(ctx context.Context, id uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, apperr.WrapDBError(err)
	}
	return count > 0, nil
}

// DeleteWithAssociations deletes a node and its associated PolicyNode, Task,
// and Alert records in a single transaction.
func (r *NodeRepository) DeleteWithAssociations(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var node model.Node
		if err := tx.First(&node, id).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.PolicyNode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.Task{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.Alert{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Node{}, id).Error
	})
}

// BatchDeleteWithAssociations deletes multiple nodes and their associated
// records. Returns the number of deleted nodes and IDs that were not found.
func (r *NodeRepository) BatchDeleteWithAssociations(ctx context.Context, ids []uint) (int64, []uint, error) {
	var deleted int64
	var notFound []uint

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingIDs []uint
		if err := tx.Model(&model.Node{}).Where("id IN ?", ids).Pluck("id", &existingIDs).Error; err != nil {
			return err
		}

		existSet := make(map[uint]struct{}, len(existingIDs))
		for _, eid := range existingIDs {
			existSet[eid] = struct{}{}
		}
		for _, id := range ids {
			if _, ok := existSet[id]; !ok {
				notFound = append(notFound, id)
			}
		}

		if len(existingIDs) == 0 {
			return nil
		}

		if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.PolicyNode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Task{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id IN ?", existingIDs).Delete(&model.Alert{}).Error; err != nil {
			return err
		}

		result := tx.Where("id IN ?", existingIDs).Delete(&model.Node{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return deleted, notFound, nil
}

// FindSSHKeyByID looks up an SSH key by its primary key.
func (r *NodeRepository) FindSSHKeyByID(ctx context.Context, id uint) (*model.SSHKey, error) {
	var key model.SSHKey
	if err := r.db.WithContext(ctx).First(&key, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &key, nil
}
