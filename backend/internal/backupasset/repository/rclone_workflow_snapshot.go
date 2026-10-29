package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func revalidateRcloneWorkflowPlanTx(ctx context.Context, tx *gorm.DB, snapshot rcloneBindingWorkflowPlan) error {
	_, err := lockRcloneWorkflowPlanTx(ctx, tx, snapshot, false)
	return err
}

// Preview only locates the repository; authority is established by the ordered
// locks and comparison below. This matches publication and retained-point work.
func lockRcloneWorkflowPlanTx(ctx context.Context, tx *gorm.DB, snapshot rcloneBindingWorkflowPlan, requireNoRuns bool) (rcloneBindingWorkflowPlan, error) {
	var current rcloneBindingWorkflowPlan
	var preview model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", snapshot.task.ID).First(&preview).Error; err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	if preview.ID != snapshot.link.ID || preview.RepositoryID != snapshot.repository.ID {
		return current, fmt.Errorf("%w: Rclone workflow link changed", backupasset.ErrConflict)
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&current.repository, "id = ?", preview.RepositoryID).Error; err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND archived_at IS NULL", snapshot.task.ID).First(&current.task).Error; err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	if requireNoRuns {
		var active []model.TaskRun
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND status IN ?", current.task.ID, model.TaskRunActiveStatuses()).Find(&active).Error; err != nil {
			return current, err
		}
		if len(active) != 0 {
			return current, fmt.Errorf("%w: Rclone Task has an active run", backupasset.ErrConflict)
		}
	}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND task_id = ? AND unlinked_at IS NULL", preview.ID, current.task.ID).First(&current.link).Error; err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	if err := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND repository_id = ? AND status = ?", snapshot.binding.ID, current.repository.ID, bindingStatusActive).First(&current.binding).Error; err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	current.bindingCiphertext = current.binding.EncryptedConfig
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &current.task); err != nil {
		return current, rcloneWorkflowSnapshotError(err)
	}
	revision, err := managedRsyncTaskRevision(current.task)
	if err != nil {
		return current, err
	}
	expectedRevision, err := managedRsyncTaskRevision(snapshot.task)
	if err != nil {
		return current, err
	}
	if revision != expectedRevision || current.task.NodeID != snapshot.task.NodeID ||
		bindingProviderForTask(current.task) != bindingProviderForTask(snapshot.task) ||
		current.task.RsyncTarget != snapshot.task.RsyncTarget || current.task.ExecutorConfig != snapshot.task.ExecutorConfig ||
		!sameConnectNodeLineage(snapshot.task.Node, current.task.Node) ||
		(snapshot.task.Node.SSHKey == nil) != (current.task.Node.SSHKey == nil) ||
		(snapshot.task.Node.SSHKey != nil && !sameConnectSSHCredentialLineage(*snapshot.task.Node.SSHKey, *current.task.Node.SSHKey)) ||
		!reflect.DeepEqual(current.link, snapshot.link) || current.bindingCiphertext != snapshot.bindingCiphertext ||
		current.binding.ConfigFingerprint != snapshot.binding.ConfigFingerprint || current.binding.BindingKind != snapshot.binding.BindingKind ||
		current.repository.ID != snapshot.repository.ID || current.repository.ProviderKind != snapshot.repository.ProviderKind ||
		current.repository.VersionMode != snapshot.repository.VersionMode || current.repository.ImmutabilityLevel != snapshot.repository.ImmutabilityLevel ||
		current.repository.Status != snapshot.repository.Status || current.repository.CapabilityRevision != snapshot.repository.CapabilityRevision ||
		!reflect.DeepEqual(current.repository.RepositoryIdentity, snapshot.repository.RepositoryIdentity) {
		return rcloneBindingWorkflowPlan{}, fmt.Errorf("%w: Rclone workflow snapshot changed", backupasset.ErrConflict)
	}
	// Preserve decoded binding data for callers that subsequently save the row.
	current.binding.EncryptedConfig = snapshot.binding.EncryptedConfig
	current.legacyBinding = snapshot.legacyBinding
	current.managedBinding = snapshot.managedBinding
	current.bindingRevision = snapshot.bindingRevision
	return current, nil
}

func rcloneWorkflowSnapshotError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: Rclone workflow snapshot no longer exists", backupasset.ErrConflict)
	}
	return err
}
