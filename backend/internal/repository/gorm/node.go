package gorm

import (
	"context"
	"database/sql"
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
	if node.SSHKeyID == nil || *node.SSHKeyID == 0 {
		return apperr.WrapDBError(r.db.WithContext(ctx).Create(node).Error)
	}
	return r.withPinnedSSHKey(ctx, node, false, false, func(tx *gorm.DB, _ *model.Node) error {
		return tx.Create(node).Error
	})
}

// withPinnedSSHKey fences node writes against key rotation. Updates lock the
// node row before the key; timestamp updates read their projection there too.
func (r *NodeRepository) withPinnedSSHKey(ctx context.Context, node *model.Node, update, readTimes bool, write func(*gorm.DB, *model.Node) error) error {
	var options *sql.TxOptions
	if r.db.Name() == "postgres" {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		switch tx.Name() {
		case "postgres":
			if err := tx.Exec("LOCK TABLE nodes IN ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
		case "sqlite":
			if err := tx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
				return err
			}
		}
		var existing model.Node
		if update {
			fields := []string{"id"}
			if readTimes {
				fields = append(fields, "maintenance_start", "maintenance_end", "expiry_date")
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select(fields).First(&existing, node.ID).Error; err != nil {
				return err
			}
		}
		if node.SSHKeyID != nil && *node.SSHKeyID > 0 {
			var key model.SSHKey
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&key, *node.SSHKeyID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return repository.ErrNodeSSHKeyNotFound
				}
				return err
			}
		}
		return write(tx, &existing)
	}, options)
	return apperr.WrapDBError(err)
}

// Update writes configuration fields without overwriting probe or backup results.
func (r *NodeRepository) Update(ctx context.Context, node *model.Node, updateStatus bool, applyTimes func(*model.Node) error) error {
	fields := []string{"Name", "Host", "Port", "Username", "AuthType", "Password", "PrivateKey", "SSHKeyID", "Tags", "BasePath", "BackupDir", "UseSudo", "Archived", "EscalationPolicyID", "UpdatedAt"}
	if updateStatus {
		fields = append(fields, "Status")
	}
	write := func(tx *gorm.DB, existing *model.Node) error {
		if applyTimes != nil {
			start, end, expiry := captureNodeTime(existing.MaintenanceStart), captureNodeTime(existing.MaintenanceEnd), captureNodeTime(existing.ExpiryDate)
			if err := applyTimes(existing); err != nil {
				return err
			}
			if start.changed(existing.MaintenanceStart) {
				fields = append(fields, "MaintenanceStart")
				node.MaintenanceStart = existing.MaintenanceStart
			}
			if end.changed(existing.MaintenanceEnd) {
				fields = append(fields, "MaintenanceEnd")
				node.MaintenanceEnd = existing.MaintenanceEnd
			}
			if expiry.changed(existing.ExpiryDate) {
				fields = append(fields, "ExpiryDate")
				node.ExpiryDate = existing.ExpiryDate
			}
		}
		result := tx.Model(node).Select(fields).Updates(node)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	if applyTimes != nil || (node.SSHKeyID != nil && *node.SSHKeyID > 0) {
		return r.withPinnedSSHKey(ctx, node, true, applyTimes != nil, write)
	}
	return apperr.WrapDBError(write(r.db.WithContext(ctx), nil))
}

type nodeTimeSnapshot struct {
	value time.Time
	valid bool
}

func captureNodeTime(value *time.Time) nodeTimeSnapshot {
	if value == nil {
		return nodeTimeSnapshot{}
	}
	return nodeTimeSnapshot{value: *value, valid: true}
}

func (snapshot nodeTimeSnapshot) changed(value *time.Time) bool {
	return snapshot.valid != (value != nil) || (snapshot.valid && !snapshot.value.Equal(*value))
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
	var options *sql.TxOptions
	if r.db.Name() == "postgres" {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockNodeDeletionSpace(tx); err != nil {
			return err
		}
		var node model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&node, id).Error; err != nil {
			return err
		}
		if err := checkNodeDrillTargets(tx, []uint{id}); err != nil {
			return err
		}
		if err := lockNodeDeletionTasks(tx, []uint{id}); err != nil {
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
	}, options)
}

// Lock ownership before nodes and fence rotation before taking node row locks.
func lockNodeDeletionSpace(tx *gorm.DB) error {
	if err := policy.LockTargetOwnershipSpace(tx); err != nil {
		return err
	}
	if tx.Name() == "postgres" {
		return tx.Exec("LOCK TABLE nodes IN ROW EXCLUSIVE MODE").Error
	}
	return nil
}

// Batch retry locks tasks before nodes. Do not wait on its task rows while
// holding nodes, or the two paths can deadlock.
func lockNodeDeletionTasks(tx *gorm.DB, ids []uint) error {
	if tx.Name() != "postgres" {
		return nil
	}
	var tasks []model.Task
	return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).
		Select("id").Where("node_id IN ?", ids).Order("id").Find(&tasks).Error
}

// TriggerDrill and the drill scanner use drill_enabled independently of the
// policy's enabled/template flags. Disabled drills may retain legacy targets.
func checkNodeDrillTargets(tx *gorm.DB, ids []uint) error {
	var count int64
	if err := tx.Model(&model.Policy{}).Where("drill_enabled = ? AND drill_target_node_id IN ?", true, ids).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return repository.ErrNodeDrillTargetInUse
	}
	return nil
}

// BatchDeleteWithAssociations deletes multiple nodes and their associated
// records. Returns the number of deleted nodes and IDs that were not found.
func (r *NodeRepository) BatchDeleteWithAssociations(ctx context.Context, ids []uint) (int64, []uint, error) {
	var deleted int64
	var notFound []uint

	var options *sql.TxOptions
	if r.db.Name() == "postgres" {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockNodeDeletionSpace(tx); err != nil {
			return err
		}
		ordered := append([]uint(nil), ids...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
		var existingIDs []uint
		for i, id := range ordered {
			if i > 0 && ordered[i-1] == id {
				continue
			}
			var node model.Node
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&node, id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			existingIDs = append(existingIDs, node.ID)
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
		if err := checkNodeDrillTargets(tx, existingIDs); err != nil {
			return err
		}
		if err := lockNodeDeletionTasks(tx, existingIDs); err != nil {
			return err
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
	}, options)
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
