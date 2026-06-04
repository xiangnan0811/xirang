package gorm

import (
	"context"

	"gorm.io/gorm"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	"xirang/backend/internal/repository"
)

var _ repository.PolicyRepository = (*PolicyRepository)(nil)

// PolicyRepository implements repository.PolicyRepository using GORM.
type PolicyRepository struct {
	db *gorm.DB
}

// NewPolicyRepository creates a new PolicyRepository.
func NewPolicyRepository(db *gorm.DB) *PolicyRepository {
	return &PolicyRepository{db: db}
}

// FindByID returns a policy by its primary key.
func (r *PolicyRepository) FindByID(ctx context.Context, id uint) (*model.Policy, error) {
	var policy model.Policy
	if err := r.db.WithContext(ctx).First(&policy, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &policy, nil
}

// FindByIDWithNodes returns a policy by its primary key with Nodes preloaded.
func (r *PolicyRepository) FindByIDWithNodes(ctx context.Context, id uint) (*model.Policy, error) {
	var policy model.Policy
	if err := r.db.WithContext(ctx).Preload("Nodes").First(&policy, id).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return &policy, nil
}

// List returns all policies.
func (r *PolicyRepository) List(ctx context.Context) ([]model.Policy, error) {
	var policies []model.Policy
	if err := r.db.WithContext(ctx).Find(&policies).Error; err != nil {
		return nil, apperr.WrapDBError(err)
	}
	return policies, nil
}

// Create inserts a new policy.
func (r *PolicyRepository) Create(ctx context.Context, policy *model.Policy) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Create(policy).Error)
}

// Update saves changes to an existing policy.
func (r *PolicyRepository) Update(ctx context.Context, policy *model.Policy) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Save(policy).Error)
}

// Delete removes a policy by its primary key.
func (r *PolicyRepository) Delete(ctx context.Context, id uint) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Delete(&model.Policy{}, id).Error)
}

// ExistsByID returns true if a policy with the given id exists.
func (r *PolicyRepository) ExistsByID(ctx context.Context, id uint) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Policy{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, apperr.WrapDBError(err)
	}
	return count > 0, nil
}

// CountByID returns the count of policies matching the id.
func (r *PolicyRepository) CountByID(ctx context.Context, id uint) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&model.Policy{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return 0, apperr.WrapDBError(err)
	}
	return count, nil
}

// CreatePolicyNode creates a policy-node association.
func (r *PolicyRepository) CreatePolicyNode(ctx context.Context, pn *model.PolicyNode) error {
	return apperr.WrapDBError(r.db.WithContext(ctx).Create(pn).Error)
}

// Transaction executes fn within a database transaction. The fn receives
// a new PolicyRepository that operates within the transaction.
func (r *PolicyRepository) Transaction(ctx context.Context, fn func(repo repository.PolicyRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &PolicyRepository{db: tx}
		return fn(txRepo)
	})
}
