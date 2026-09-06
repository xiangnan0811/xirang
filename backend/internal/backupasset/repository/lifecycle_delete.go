package repository

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func lifecycleDeletionUnavailable() error {
	return &provider.CapabilityError{
		Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityDeletionUnavailable},
	}
}

func lifecycleDeleteIdentityConflict(reason string) error {
	return fmt.Errorf("%w: %s", provider.ErrDeletePointIdentityConflict, reason)
}

func lifecycleDeleteRuntimeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, provider.ErrDeletePointIdentityConflict) || errors.Is(err, backupasset.ErrConflict) {
		return lifecycleDeleteIdentityConflict("lifecycle deletion runtime identity changed")
	}
	return lifecycleDeletionUnavailable()
}

func lifecycleDeleteLocator(point model.RecoveryPoint) string {
	if strings.TrimSpace(point.EncryptedProviderLocator) != "" {
		return point.EncryptedProviderLocator
	}
	return strings.TrimSpace(point.EncryptedRollbackLocator)
}

func (service *Service) lifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	switch backupasset.ProviderKind(repository.ProviderKind) {
	case backupasset.ProviderRestic:
		return service.resticLifecycleDeleteAccessTx(ctx, tx, repository, point, native)
	case backupasset.ProviderRsync:
		return service.rsyncLifecycleDeleteAccessTx(ctx, tx, repository, point, native)
	case backupasset.ProviderRclone:
		return service.rcloneLifecycleDeleteAccessTx(ctx, tx, repository, point, native)
	default:
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
}

func lifecycleRepositoryIdentity(repository model.BackupRepository) string {
	if repository.RepositoryIdentity == nil {
		return ""
	}
	return strings.TrimSpace(*repository.RepositoryIdentity)
}

// lifecycleLinkTaskIDMatches treats the nullable TaskID on a historical link
// as an optional foreign-key convenience, not as deletion authority. Once a
// link is unlinked the database may clear this field, while the link ID and
// frozen snapshots remain the durable lineage.
func lifecycleLinkTaskIDMatches(link model.TaskRepositoryLink, taskID uint) bool {
	return link.TaskID == nil || *link.TaskID == taskID
}

// lifecycleFrozenSnapshotsMatch treats the point and link name snapshots as
// required immutable deletion authority. Current Task/Node names are mutable
// presentation fields and therefore must not gate retained-point deletion.
func lifecycleFrozenSnapshotsMatch(point model.RecoveryPoint, link model.TaskRepositoryLink, task model.Task) bool {
	return task.ID != 0 && task.NodeID != 0 &&
		point.ProducingTaskNameSnapshot != "" &&
		point.ProducingNodeNameSnapshot != "" &&
		link.TaskNameSnapshot != "" &&
		link.NodeNameSnapshot != "" &&
		link.NodeIDSnapshot == task.NodeID &&
		point.ProducingNodeIDSnapshot == link.NodeIDSnapshot &&
		point.ProducingTaskNameSnapshot == link.TaskNameSnapshot &&
		point.ProducingNodeNameSnapshot == link.NodeNameSnapshot
}
func lifecycleRcloneNativeCommitKey(
	locator managedRclonePointLocatorV1,
	binding managedRcloneBindingDocumentV3,
	attempt provider.RcloneAttemptV1,
) string {
	if locator.Version == managedRclonePointLocatorLegacyVersion {
		return strings.TrimSpace(locator.NativeCommitKey)
	}
	return managedRcloneNativeControlCommitKey(binding, attempt)
}

// lockLifecycleTaskNodeSSHKeyTx materializes the exact remote command
// authority while holding row locks through the caller-owned transaction.
// GORM Preload issues separate SELECTs and does not reliably propagate a
// FOR UPDATE clause to associations, so every authority row is locked
// explicitly here.
func lockLifecycleTaskNodeSSHKeyTx(ctx context.Context, tx *gorm.DB, task *model.Task) error {
	if tx == nil || task == nil || task.ID == 0 || task.NodeID == 0 {
		return fmt.Errorf("%w: lifecycle Task/Node authority is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var node model.Node
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", task.NodeID).First(&node).Error; err != nil {
		return fmt.Errorf("lock lifecycle Node: %w", err)
	}
	if node.ID != task.NodeID {
		return fmt.Errorf("%w: lifecycle Task/Node identity changed", backupasset.ErrConflict)
	}
	if node.SSHKeyID != nil {
		var key model.SSHKey
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", *node.SSHKeyID).First(&key).Error; err != nil {
			return fmt.Errorf("lock lifecycle SSH key: %w", err)
		}
		node.SSHKey = &key
	}
	task.Node = node
	return nil
}

func (service *Service) resticLifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil || point.ProducingTaskID == nil || *point.ProducingTaskID == 0 ||
		point.ProducingTaskRunID == nil || *point.ProducingTaskRunID == 0 {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var lockedRepository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", repository.ID).First(&lockedRepository).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if lockedRepository.ID != repository.ID || lockedRepository.ProviderKind != string(backupasset.ProviderRestic) ||
		lockedRepository.VersionMode != string(backupasset.VersionNativeSnapshot) ||
		lockedRepository.CapabilityRevision <= 0 || lockedRepository.CapabilityRevision != point.CapabilityRevision ||
		lockedRepository.RepositoryIdentity == nil ||
		lifecycleRepositoryIdentity(lockedRepository) != lifecycleRepositoryIdentity(repository) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle repository identity changed")
	}
	repository = lockedRepository
	locator, err := decodeResticPointLocator(lifecycleDeleteLocator(point))
	if err != nil || native != locator.FullSnapshotID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle locator identity changed")
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) ||
		lineage.TaskID != *point.ProducingTaskID || lineage.TaskRunID != *point.ProducingTaskRunID ||
		backupasset.ValidateOpaqueID(lineage.TaskRepositoryLinkID) != nil {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle lineage changed")
	}
	var task model.Task
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", lineage.TaskID).First(&task).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if bindingProviderForTask(task) != backupasset.ProviderRestic || task.NodeID == 0 {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle Task identity changed")
	}
	var link model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ?",
			lineage.TaskRepositoryLinkID, repository.ID).First(&link).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if !lifecycleLinkTaskIDMatches(link, task.ID) ||
		link.PublicationMode != string(backupasset.PublicationNativeSnapshot) ||
		!lifecycleFrozenSnapshotsMatch(point, link, task) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle link identity changed")
	}
	var binding model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil || document.Provider != backupasset.ProviderRestic {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding lineage changed")
	}
	// The active binding document names the repository binding owner. It is
	// deliberately distinct from the producing Task: publication execution
	// derives the producer's current access using this document's immutable
	// repository salt and native identity. A different owner is authorized only
	// while an active or historical native link proves that it belongs to this
	// repository; an arbitrary replacement Task must remain fail-closed.
	if document.TaskID == 0 || document.NodeID == 0 {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner identity changed")
	}
	if document.TaskID == task.ID {
		if document.NodeID != task.NodeID {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner identity changed")
		}
	} else {
		var bindingOwner model.Task
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", document.TaskID).First(&bindingOwner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner identity changed")
			}
			return provider.AccessBinding{}, lifecycleDeletionUnavailable()
		}
		if bindingProviderForTask(bindingOwner) != backupasset.ProviderRestic ||
			bindingOwner.NodeID != document.NodeID {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner identity changed")
		}
		var ownerLink model.TaskRepositoryLink
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND repository_id = ? AND publication_mode = ? AND node_id_snapshot = ?",
				bindingOwner.ID, repository.ID, string(backupasset.PublicationNativeSnapshot), document.NodeID).
			Order("CASE WHEN unlinked_at IS NULL THEN 0 ELSE 1 END ASC, id ASC").
			First(&ownerLink).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner link changed")
			}
			return provider.AccessBinding{}, lifecycleDeletionUnavailable()
		}
		if ownerLink.TaskID == nil || *ownerLink.TaskID != bindingOwner.ID ||
			ownerLink.RepositoryID != repository.ID ||
			ownerLink.PublicationMode != string(backupasset.PublicationNativeSnapshot) ||
			ownerLink.NodeIDSnapshot != document.NodeID {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding owner link changed")
		}
	}
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &task); err != nil {
		if errors.Is(err, backupasset.ErrConflict) {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle Node identity changed")
		}
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if !lifecycleFrozenSnapshotsMatch(point, link, task) ||
		point.ProducingNodeIDSnapshot != task.Node.ID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle immutable snapshots changed")
	}
	var taskRun model.TaskRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ?", lineage.TaskRunID, task.ID).First(&taskRun).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if !authoritativeTaskRunForTask(taskRun, task) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle TaskRun identity changed")
	}
	var access provider.AccessBinding
	if document.TaskID == task.ID {
		// The binding document is the immutable credential snapshot for its
		// owner. Archived or renamed owner Tasks must not redirect deletion to
		// their mutable current locator or password.
		access, err = accessFromBindingDocument(document, task.Node)
		if err == nil {
			access.RepositoryID = repository.ID
		}
	} else {
		// A shared repository binding carries only repository identity for a
		// distinct producer; deletion must use that producer's current access.
		access, err = publicationAccessForCurrentTask(document, repository, task)
	}
	if err != nil {
		if errors.Is(err, backupasset.ErrConflict) {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle binding identity changed")
		}
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if access.Provider != backupasset.ProviderRestic || access.RepositoryID != repository.ID ||
		access.TaskID != task.ID || access.NodeID != task.NodeID ||
		strings.TrimSpace(access.Locator) == "" || len(access.Secret) == 0 {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle Task access changed")
	}
	runtimeAccess, ok := access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil || runtimeAccess.Command.Node.ID != task.Node.ID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle command access changed")
	}
	if repository.RepositoryIdentity == nil || runtimeAccess.NativeRepositoryID == "" ||
		*repository.RepositoryIdentity != provider.NativeResticIdentityPrefix+runtimeAccess.NativeRepositoryID ||
		document.NativeRepositoryID != runtimeAccess.NativeRepositoryID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle native identity changed")
	}
	access.RepositoryID = repository.ID
	if point.SourceFingerprint != resticSourceFingerprint(lifecycleRepositoryIdentity(repository), native) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Restic lifecycle point source identity changed")
	}
	return access, nil
}

// Rsync lifecycle resolution is ordered repository -> Task -> TaskRun -> link
// -> binding -> Node -> SSH key to match publication preparation.
func (service *Service) rsyncLifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil || service.publication == nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	decoded, err := decodeManagedRsyncPointLocator(lifecycleDeleteLocator(point))
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(decoded.TaggedAttempt)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if native != attempt.FinalComponent || decoded.RepositoryID != repository.ID ||
		decoded.RecoveryPointID != point.ID || attempt.RepositoryID != repository.ID ||
		attempt.RecoveryPointID != point.ID || decoded.FinalComponent != point.ID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync locator identity changed")
	}
	var lockedRepository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", repository.ID).First(&lockedRepository).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if lockedRepository.ID != repository.ID ||
		lockedRepository.ProviderKind != string(backupasset.ProviderRsync) ||
		lockedRepository.CapabilityRevision != repository.CapabilityRevision ||
		lifecycleRepositoryIdentity(lockedRepository) != lifecycleRepositoryIdentity(repository) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync repository identity changed")
	}
	repository = lockedRepository
	var task model.Task
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", attempt.TaskID).First(&task).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if bindingProviderForTask(task) != backupasset.ProviderRsync || task.NodeID == 0 {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync lifecycle Task identity changed")
	}
	var taskRun model.TaskRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ?", attempt.TaskRunID, task.ID).First(&taskRun).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync TaskRun authority changed")
		}
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	var link model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ?",
			attempt.TaskRepositoryLinkID, repository.ID).First(&link).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	var bindingRow model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&bindingRow).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	stored, err := decodeStoredBindingDocument(bindingRow.EncryptedConfig)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	binding := *stored.ManagedRsyncV2
	if err := binding.validateForAttempt(attempt); err != nil {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync binding and attempt identity changed")
	}
	expectedRepositoryIdentity, err := managedRsyncRepositoryIdentity(binding)
	if err != nil || repository.RepositoryIdentity == nil ||
		lifecycleRepositoryIdentity(repository) != expectedRepositoryIdentity {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync repository identity changed")
	}
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &task); err != nil {
		if errors.Is(err, backupasset.ErrConflict) {
			return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync runtime Node identity changed")
		}
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if task.ID != attempt.TaskID || task.NodeID != binding.NodeID ||
		!authoritativeTaskRunForTask(taskRun, task) ||
		!lifecycleLinkTaskIDMatches(link, attempt.TaskID) ||
		link.ID != attempt.TaskRepositoryLinkID || link.RepositoryID != attempt.RepositoryID ||
		link.NodeIDSnapshot != task.NodeID ||
		link.PublicationMode != string(binding.PublicationMode) || link.PublicationMode != string(attempt.PublicationMode) ||
		binding.TaskID != task.ID || binding.NodeID != task.NodeID ||
		binding.RepositoryID != repository.ID || binding.TaskRepositoryLinkID != link.ID ||
		!lifecycleFrozenSnapshotsMatch(point, link, task) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync runtime lineage changed")
	}
	if point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
		point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync point lineage changed")
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
		lineage.PublicationMode != string(attempt.PublicationMode) ||
		!lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync durable lineage changed")
	}
	markerKey, err := service.publication.rsyncMarkerKeyTx(ctx, tx, repository.ID)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	source := managedRsyncSourceFingerprint(markerKey, binding, point.ID)
	if source == "" || source != point.SourceFingerprint {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync point source identity changed")
	}
	salt, err := hexDecodeSalt(binding.IdentitySalt)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rsync lifecycle binding salt changed")
	}
	return provider.AccessBinding{
		Provider:     backupasset.ProviderRsync,
		RepositoryID: repository.ID,
		TaskID:       task.ID,
		NodeID:       task.NodeID,
		IdentitySalt: salt,
		EndpointFacts: []string{
			fmt.Sprintf("task:%d", task.ID),
			fmt.Sprintf("node:%d", task.NodeID),
			"transport:local",
			"layout:" + binding.LayoutRevision,
			"managed_root_identity:" + binding.ManagedRootIdentityDigest,
		},
		AdapterData: provider.RsyncPointDeletionAccess{
			ManagedRoot:        filepath.Clean(binding.ManagedRootLocator),
			MarkerKey:          markerKey,
			Attempt:            attempt,
			CommitMarkerDigest: decoded.CommitMarkerDigest,
			SourceFingerprint:  source,
			Command:            &provider.RemoteCommandAccess{Node: task.Node},
		},
	}, nil
}

func (service *Service) rcloneLifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil || service.publication == nil ||
		point.ProducingTaskID == nil || *point.ProducingTaskID == 0 {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	locator, err := decodeManagedRclonePointLocator(lifecycleDeleteLocator(point))
	if err != nil {
		if errors.Is(err, provider.ErrDeletePointIdentityConflict) {
			return provider.AccessBinding{}, err
		}
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if locator.Provider != backupasset.ProviderRclone || locator.RepositoryID != repository.ID ||
		locator.RecoveryPointID != point.ID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone locator identity changed")
	}
	switch locator.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		return service.rclonePrefixLifecycleDeleteAccessTx(ctx, tx, repository, point, native, locator)
	case backupasset.PublicationNativeObjectVersions:
		return service.rcloneNativeLifecycleDeleteAccessTx(ctx, tx, repository, point, native, locator)
	default:
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
}

// loadExactManagedRcloneLifecycleRuntimeTx is intentionally separate from the
// publication loader. A retained point may outlive its publication preflight,
// but it must still use the same immutable binding, lineage, capability, and
// rollback evidence. The lock order is repository -> Task -> TaskRun -> link
// -> binding -> Node -> SSH key and all locks remain held by the caller's
// transaction.
func (service *PublicationService) loadExactManagedRcloneLifecycleRuntimeTx(
	ctx context.Context,
	tx *gorm.DB,
	repositoryID string,
	taskID uint,
	taskRunID uint,
	linkID string,
) (managedRclonePublicationRuntime, error) {
	if service == nil || tx == nil || backupasset.ValidateOpaqueID(repositoryID) != nil || taskID == 0 ||
		taskRunID == 0 || backupasset.ValidateOpaqueID(linkID) != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle runtime is unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var repository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", repositoryID).First(&repository).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle repository", backupasset.ErrNotFound)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone lifecycle repository: %w", err)
	}
	var taskEntity model.Task
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", taskID).First(&taskEntity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle Task", backupasset.ErrNotFound)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone lifecycle Task: %w", err)
	}
	if bindingProviderForTask(taskEntity) != backupasset.ProviderRclone {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle Task provider changed", backupasset.ErrConflict)
	}
	var taskRun model.TaskRun
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND task_id = ?", taskRunID, taskEntity.ID).First(&taskRun).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle TaskRun authority changed", backupasset.ErrConflict)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone lifecycle TaskRun: %w", err)
	}
	if taskRun.ID != taskRunID || taskRun.TaskID != taskEntity.ID ||
		!authoritativeTaskRunForTask(taskRun, taskEntity) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle TaskRun authority changed", backupasset.ErrConflict)
	}
	var link model.TaskRepositoryLink
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND repository_id = ?", linkID, repositoryID).
		First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle link", backupasset.ErrNotFound)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone lifecycle link: %w", err)
	}
	mode := backupasset.TaskPublicationMode(link.PublicationMode)
	version, semantics, state, err := backupasset.MapPublicationMode(backupasset.ProviderRclone, mode)
	if err != nil || semantics != backupasset.PointXirangManifest || state != backupasset.RecoveryPointPreparing {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle link mode is invalid", backupasset.ErrConflict)
	}
	if repository.ProviderKind != string(backupasset.ProviderRclone) || repository.VersionMode != string(version) ||
		repository.ImmutabilityLevel != string(rcloneImmutability(mode)) || repository.RepositoryIdentity == nil ||
		repository.Status != string(backupasset.RepositoryOnline) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle repository contract mismatch", backupasset.ErrConflict)
	}
	var binding model.RepositoryAccessBinding
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND status = ?", repository.ID, bindingStatusActive).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle binding", backupasset.ErrConflict)
		}
		return managedRclonePublicationRuntime{}, fmt.Errorf("lock managed Rclone lifecycle binding: %w", err)
	}
	stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if stored.ManagedRcloneV3 == nil || stored.V1 != nil || stored.ManagedRsyncV2 != nil {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone V3 lifecycle binding required", backupasset.ErrConflict)
	}
	document := *stored.ManagedRcloneV3
	if err := validateManagedRcloneBindingDocumentV3(document); err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if document.TaskID != taskEntity.ID || document.NodeID != taskEntity.NodeID ||
		document.RepositoryID != repository.ID || document.TaskRepositoryLinkID != link.ID ||
		!lifecycleLinkTaskIDMatches(link, taskEntity.ID) ||
		link.NodeIDSnapshot != taskEntity.NodeID ||
		link.PublicationMode != string(document.PublicationMode) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle binding lineage drift", backupasset.ErrConflict)
	}
	if document.RollbackPrepared || document.CapabilityRevision != uint64(repository.CapabilityRevision) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle preflight or rollback state is not executable", backupasset.ErrForbidden)
	}
	if err := lockLifecycleTaskNodeSSHKeyTx(ctx, tx, &taskEntity); err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if !authoritativeTaskRunForTask(taskRun, taskEntity) {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle TaskRun authority changed", backupasset.ErrConflict)
	}
	if !lifecycleLinkTaskIDMatches(link, taskEntity.ID) || link.RepositoryID != repository.ID ||
		link.NodeIDSnapshot != taskEntity.NodeID {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle link snapshot changed", backupasset.ErrConflict)
	}
	expectedIdentity, err := managedRcloneRepositoryIdentity(document)
	if err != nil {
		return managedRclonePublicationRuntime{}, err
	}
	if *repository.RepositoryIdentity != expectedIdentity {
		return managedRclonePublicationRuntime{}, fmt.Errorf("%w: managed Rclone lifecycle repository identity drift", backupasset.ErrConflict)
	}
	return managedRclonePublicationRuntime{
		repository: repository, task: taskEntity, link: link, binding: document,
	}, nil
}

type rcloneLifecycleRuntimeEvidence struct {
	runtime     managedRclonePublicationRuntime
	attempt     provider.RcloneAttemptV1
	commit      provider.RcloneCommitV1
	consistency backupasset.PublicationConsistencyV1
	markerKey   []byte
}

// Runtime authority is fully locked and closed before marker keys, durable
// evidence, or any provider client construction is attempted.
func (service *Service) loadRcloneLifecycleRuntimeEvidenceTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	locator managedRclonePointLocatorV1,
) (rcloneLifecycleRuntimeEvidence, error) {
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeletionUnavailable()
	}
	if locator.AttemptID != attempt.AttemptID || locator.RecoveryPointID != attempt.RecoveryPointID ||
		locator.RepositoryID != attempt.RepositoryID || locator.RepositoryID != repository.ID ||
		locator.RecoveryPointID != point.ID || locator.PublicationMode != attempt.PublicationMode ||
		point.ProducingTaskID == nil || *point.ProducingTaskID != attempt.TaskID ||
		point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != attempt.TaskRunID {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone locator or point identity changed")
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || lineage.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		lineage.TaskID != attempt.TaskID || lineage.TaskRunID != attempt.TaskRunID ||
		lineage.PublicationMode != string(attempt.PublicationMode) ||
		!lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone durable lineage changed")
	}
	runtime, err := service.publication.loadExactManagedRcloneLifecycleRuntimeTx(
		ctx, tx, repository.ID, attempt.TaskID, attempt.TaskRunID, attempt.TaskRepositoryLinkID,
	)
	if err != nil {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteRuntimeError(err)
	}
	runtimeForValidation := runtime
	if runtimeForValidation.link.TaskID == nil {
		taskID := attempt.TaskID
		runtimeForValidation.link.TaskID = &taskID
	}
	if err := validateRcloneReconcileRuntime(runtimeForValidation, point, attempt); err != nil {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone runtime identity changed")
	}
	if !lifecycleFrozenSnapshotsMatch(point, runtime.link, runtime.task) {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone lifecycle immutable snapshots changed")
	}
	if runtime.repository.ID != repository.ID ||
		runtime.repository.CapabilityRevision != repository.CapabilityRevision ||
		runtime.repository.RepositoryIdentity == nil ||
		lifecycleRepositoryIdentity(runtime.repository) != lifecycleRepositoryIdentity(repository) {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone repository runtime changed")
	}
	markerKey, err := service.publication.rcloneMarkerKeyTx(ctx, tx, repository.ID)
	if err != nil {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeletionUnavailable()
	}
	commitEnvelope, err := provider.DecodeProviderCommit(locator.TaggedCommit)
	if err != nil {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone commit evidence changed")
	}
	commit, err := commitEnvelope.RcloneCommit()
	if err != nil || commit.RepositoryID != attempt.RepositoryID ||
		commit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		commit.RecoveryPointID != attempt.RecoveryPointID || commit.AttemptID != attempt.AttemptID ||
		commit.PublicationMode != attempt.PublicationMode ||
		!commit.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) ||
		commit.ChildFenceDigest != attempt.ChildFenceDigest {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone commit lineage changed")
	}
	commitDigest, err := canonicalRcloneProviderCommitDigest(commit)
	if err != nil || commitDigest != locator.ProviderCommitDigest {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone commit digest changed")
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil || consistency.Provider != backupasset.ProviderRclone ||
		consistency.CapabilityRevision != point.CapabilityRevision ||
		consistency.RepositoryIdentityDigest != attempt.RepositoryIdentityDigest ||
		consistency.ProviderCommitDigest != locator.ProviderCommitDigest ||
		point.ManifestDigest != commit.ManifestIndexDigest ||
		point.EntryCount != int64(commit.ManifestEntryCount) ||
		point.LogicalBytes != int64(commit.LogicalBytes) {
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone durable evidence changed")
	}
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedPrefix:
		if commit.Portable == nil || commit.Native != nil || attempt.Portable == nil ||
			commit.Portable.AttemptMarkerDigest != attempt.Portable.AttemptMarkerDigest ||
			commit.Portable.CommitPayloadDigest != locator.CommitPayloadDigest ||
			commit.Portable.ControlIdentityDigest != locator.ManifestControlIdentity ||
			managedRclonePortableAttemptRoot(runtime.binding, attempt) != locator.PortableAttemptRoot {
			return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone portable evidence changed")
		}
	case backupasset.PublicationNativeObjectVersions:
		if commit.Native == nil || commit.Portable != nil || attempt.Native == nil || runtime.binding.Native == nil ||
			commit.Native.CommitContentDigest != locator.CommitPayloadDigest ||
			commit.Native.ManifestControlGraphDigest != locator.ManifestControlIdentity ||
			commit.Native.CapabilityRevision != attempt.CapabilityRevision ||
			commit.Native.CredentialRevision != attempt.CredentialRevision ||
			commit.Native.KMSCapabilityRevision != attempt.Native.KMSCapabilityRevision ||
			commit.Native.VersioningDigest != attempt.Native.VersioningDigest ||
			commit.Native.LifecycleDigest != attempt.Native.LifecycleDigest ||
			commit.Native.BucketEncryptionDigest != attempt.Native.BucketEncryptionDigest ||
			commit.Native.ActiveKeyDigest != runtime.binding.Native.ActiveKMSKeyDigest ||
			commit.Native.RetainedReadKeySetDigest != attempt.Native.RetainedReadKeySetDigest ||
			commit.Native.RoleSessionIdentityDigest != attempt.Native.RoleSessionIdentityDigest {
			return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone native evidence changed")
		}
		if locator.Version == managedRclonePointLocatorLegacyVersion {
			if strings.TrimSpace(locator.NativeCommitKey) == "" || strings.TrimSpace(locator.NativeCommitVersionID) == "" {
				return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone legacy native commit identity is unavailable")
			}
			expectedSource := hex.EncodeToString(rcloneOwnershipDigest(markerKey,
				"xirang.rclone.native-point-identity.v1", attempt.RepositoryID,
				locator.NativeCommitKey, locator.NativeCommitVersionID, commit.Native.CommitContentDigest))
			if expectedSource == "" || point.SourceFingerprint != locator.PhysicalIdentityDigest ||
				point.SourceFingerprint != expectedSource {
				return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone legacy native point source identity changed")
			}
		} else {
			expectedSource := managedRcloneNativePointIdentityDigest(
				markerKey, attempt.RepositoryID, commit.Native.CommitContentDigest,
				locator.FrozenNativeVersionCount, locator.FrozenNativeVersionsDigest,
				locator.FrozenNativeReferenceCount, locator.FrozenNativeReferencesDigest,
			)
			if expectedSource == "" || point.SourceFingerprint != locator.PhysicalIdentityDigest ||
				point.SourceFingerprint != expectedSource {
				return rcloneLifecycleRuntimeEvidence{}, lifecycleDeleteIdentityConflict("Rclone native point source identity changed")
			}
		}
	default:
		return rcloneLifecycleRuntimeEvidence{}, lifecycleDeletionUnavailable()
	}
	return rcloneLifecycleRuntimeEvidence{
		runtime: runtime, attempt: attempt, commit: commit, consistency: consistency, markerKey: markerKey,
	}, nil
}

func (service *Service) rclonePrefixLifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
	locator managedRclonePointLocatorV1,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil || service.publication == nil ||
		native != locator.PortableAttemptRoot || locator.PortableAttemptRoot == "" {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	evidence, err := service.loadRcloneLifecycleRuntimeEvidenceTx(ctx, tx, repository, point, locator)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	expectedSource := hex.EncodeToString(rcloneOwnershipDigest(evidence.markerKey,
		"xirang.rclone.portable-point-identity.v1", evidence.attempt.RepositoryID,
		locator.PortableAttemptRoot, evidence.commit.Portable.CommitComponent,
		evidence.commit.Portable.CommitPayloadDigest))
	if point.SourceFingerprint != locator.PhysicalIdentityDigest || point.SourceFingerprint != expectedSource {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone portable point source identity changed")
	}
	binding := evidence.runtime.binding
	if binding.Portable == nil || binding.Portable.ConfigDigest != evidence.attempt.ConfigDigest ||
		binding.CapabilityRevision != uint64(repository.CapabilityRevision) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone portable binding identity changed")
	}
	if evidence.runtime.task.ID == 0 || evidence.runtime.task.Node.ID == 0 {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	prefix, err := provider.NewRclonePrivateLocator(locator.PortableAttemptRoot)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	salt, err := hexDecodeSalt(binding.IdentitySalt)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone portable binding salt changed")
	}
	legacy, err := decodeBindingDocument(binding.LegacyBindingV1)
	if err != nil || legacy.Provider != backupasset.ProviderRclone || legacy.TaskID != evidence.runtime.task.ID ||
		legacy.NodeID != evidence.runtime.task.NodeID || legacy.IdentitySalt != binding.IdentitySalt {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone portable endpoint binding changed")
	}
	return provider.AccessBinding{
		Provider:      backupasset.ProviderRclone,
		RepositoryID:  repository.ID,
		TaskID:        evidence.runtime.task.ID,
		NodeID:        evidence.runtime.task.NodeID,
		IdentitySalt:  salt,
		EndpointFacts: append([]string(nil), legacy.EndpointFacts...),
		Locator:       locator.PortableAttemptRoot,
		Config:        []byte(binding.Portable.BoundConfig),
		Secret:        []byte(binding.Portable.BoundConfig),
		AdapterData: provider.RclonePrefixDeletionAccess{
			Prefix: prefix, MarkerDigest: point.SourceFingerprint,
			ExpectedBackend: binding.Portable.Backend, ExpectedRootIdentity: evidence.attempt.ManagedRootIdentityDigest,
			ConfigDigest: binding.Portable.ConfigDigest, MarkerKey: append([]byte(nil), evidence.markerKey...),
			Attempt: evidence.attempt, Commit: evidence.commit, ExpectedAttemptRoot: locator.PortableAttemptRoot,
			Command: &provider.RemoteCommandAccess{Node: evidence.runtime.task.Node},
		},
	}, nil
}
func rcloneNativeLifecycleDeleteAuthorityDigest(
	markerKey []byte,
	runtime managedRclonePublicationRuntime,
	attempt provider.RcloneAttemptV1,
	locator managedRclonePointLocatorV1,
) (string, error) {
	if len(markerKey) < 32 || runtime.binding.Native == nil {
		return "", lifecycleDeleteIdentityConflict("Rclone native deletion authority is unavailable")
	}
	encodedBinding, err := encodeManagedRcloneBindingDocumentV3(runtime.binding)
	if err != nil {
		return "", lifecycleDeleteIdentityConflict("Rclone native binding authority changed")
	}
	digest := hex.EncodeToString(rcloneOwnershipDigest(
		markerKey,
		"xirang.rclone.native-deletion-authority.v1",
		runtime.repository.ID,
		fmt.Sprintf("%d", runtime.repository.CapabilityRevision),
		fmt.Sprintf("%d", runtime.task.ID),
		fmt.Sprintf("%d", runtime.task.NodeID),
		runtime.link.ID,
		attempt.AttemptID,
		attempt.RecoveryPointID,
		fmt.Sprintf("%d", attempt.TaskRunID),
		locator.ProviderCommitDigest,
		encodedBinding,
	))
	if !isLowerHex64(digest) {
		return "", lifecycleDeleteIdentityConflict("Rclone native deletion authority digest is unavailable")
	}
	return digest, nil
}

func (service *Service) rcloneNativeLifecycleDeleteAccessTx(
	ctx context.Context,
	tx *gorm.DB,
	repository model.BackupRepository,
	point model.RecoveryPoint,
	native string,
	locator managedRclonePointLocatorV1,
) (provider.AccessBinding, error) {
	if service == nil || tx == nil || service.publication == nil || service.foundation == nil ||
		service.publication.foundation == nil || native != "native-commit" {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var lockedRepository model.BackupRepository
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", repository.ID).First(&lockedRepository).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	var lockedPoint model.RecoveryPoint
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", point.ID).First(&lockedPoint).Error; err != nil {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	if lockedPoint.RepositoryID != lockedRepository.ID || lockedPoint.RepositoryID != repository.ID ||
		lockedPoint.ID != point.ID {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone native lifecycle repository or point changed")
	}
	providedPoint := point
	providedPoint.EncryptedProviderLocator = lockedPoint.EncryptedProviderLocator
	if !reflect.DeepEqual(providedPoint, lockedPoint) {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone native lifecycle point evidence changed")
	}
	repository, point = lockedRepository, lockedPoint
	evidence, err := service.loadRcloneLifecycleRuntimeEvidenceTx(ctx, tx, repository, point, locator)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	if evidence.commit.Native == nil || evidence.commit.Native.CommitKey != "" || evidence.commit.Native.CommitVersionID != "" {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone native commit key evidence changed")
	}
	commitKey := lifecycleRcloneNativeCommitKey(locator, evidence.runtime.binding, evidence.attempt)
	owned, _, err := loadManagedRcloneNativeVersionEvidenceTx(
		ctx, tx, repository.ID, point.ID, evidence.markerKey, locator, commitKey,
	)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	commitVersionID, err := managedRcloneNativeCommitVersion(owned, commitKey)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	if err := rejectManagedRcloneNativeReferenceIntersectionTx(
		ctx, tx, repository.ID, point.ID, owned, evidence.markerKey, evidence.runtime.binding,
	); err != nil {
		return provider.AccessBinding{}, err
	}
	authorityDigest, err := rcloneNativeLifecycleDeleteAuthorityDigest(
		evidence.markerKey, evidence.runtime, evidence.attempt, locator,
	)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	if err := ctx.Err(); err != nil {
		return provider.AccessBinding{}, err
	}
	now := service.now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	materializationNow := service.publication.now
	if materializationNow == nil {
		materializationNow = now
	}
	pointDeadline, ok := ctx.Deadline()
	current := now().UTC()
	if !ok || !pointDeadline.UTC().After(current) {
		return provider.AccessBinding{}, lifecycleDeletionUnavailable()
	}
	pointDeadline = pointDeadline.UTC()
	leaseConfig, err := service.foundation.LeaseConfig()
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeleteRuntimeError(err)
	}
	publicationConfig, err := service.publication.foundation.PublicationConfig()
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeleteRuntimeError(err)
	}
	lazy := &rcloneNativeLifecycleLazyExactVersionDeleter{
		snapshot: rcloneNativeLifecycleReconcileSnapshot{
			attempt:           cloneRcloneAttemptForLifecycle(evidence.attempt),
			binding:           cloneManagedRcloneBindingDocumentForLifecycle(evidence.runtime.binding),
			markerKey:         append([]byte(nil), evidence.markerKey...),
			leaseConfig:       leaseConfig,
			publicationConfig: publicationConfig,
			pointDeadline:     pointDeadline,
			commitKey:         commitKey,
			commitVersionID:   commitVersionID,
			factoryBuilder:    service.publication.rcloneNativeFactoryBuilder,
			now:               materializationNow,
		},
	}
	salt, err := hexDecodeSalt(evidence.runtime.binding.IdentitySalt)
	if err != nil {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone native binding salt changed")
	}
	legacy, err := decodeBindingDocument(evidence.runtime.binding.LegacyBindingV1)
	if err != nil || legacy.Provider != backupasset.ProviderRclone ||
		legacy.TaskID != evidence.runtime.task.ID || legacy.NodeID != evidence.runtime.task.NodeID ||
		legacy.IdentitySalt != evidence.runtime.binding.IdentitySalt {
		return provider.AccessBinding{}, lifecycleDeleteIdentityConflict("Rclone native endpoint binding changed")
	}
	return provider.AccessBinding{
		Provider:      backupasset.ProviderRclone,
		RepositoryID:  repository.ID,
		TaskID:        evidence.runtime.task.ID,
		NodeID:        evidence.runtime.task.NodeID,
		IdentitySalt:  salt,
		EndpointFacts: append([]string(nil), legacy.EndpointFacts...),
		AdapterData: provider.RcloneNativeDeletionAccess{
			Versions: owned, AuthorityDigest: authorityDigest, Client: lazy,
			Command: &provider.RemoteCommandAccess{Node: evidence.runtime.task.Node},
		},
	}, nil
}

// rcloneNativeLifecycleReconcileSnapshot contains only immutable values from
// the locked lifecycle authority. In particular, it deliberately contains no
// database handle: provider access is materialized after resolution returns.
type rcloneNativeLifecycleReconcileSnapshot struct {
	attempt           provider.RcloneAttemptV1
	binding           managedRcloneBindingDocumentV3
	markerKey         []byte
	leaseConfig       backupasset.LeaseConfig
	publicationConfig backupasset.PublicationConfig
	pointDeadline     time.Time
	commitKey         string
	commitVersionID   string
	factoryBuilder    RcloneNativeFactoryBuilder
	now               func() time.Time
}

type rcloneNativeLifecycleLazyExactVersionDeleter struct {
	snapshot rcloneNativeLifecycleReconcileSnapshot
	once     sync.Once
	client   provider.RcloneNativeExactVersionDeleter
	err      error
}

var _ provider.RcloneNativeExactVersionDeleter = (*rcloneNativeLifecycleLazyExactVersionDeleter)(nil)

func (lazy *rcloneNativeLifecycleLazyExactVersionDeleter) materialize(
	ctx context.Context,
) (provider.RcloneNativeExactVersionDeleter, error) {
	if lazy == nil {
		return nil, lifecycleDeletionUnavailable()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lazy.once.Do(func() {
		if err := ctx.Err(); err != nil {
			lazy.err = err
			return
		}
		snapshot := lazy.snapshot
		preparedAt := time.Now().UTC()
		if snapshot.now != nil {
			preparedAt = snapshot.now().UTC()
		}
		pointDeadline := snapshot.pointDeadline
		if deadline, ok := ctx.Deadline(); ok {
			pointDeadline = deadline.UTC()
		}
		if pointDeadline.IsZero() || !pointDeadline.After(preparedAt) {
			lazy.err = lifecycleDeletionUnavailable()
			return
		}
		snapshot.pointDeadline = pointDeadline
		preparer := &PublicationService{rcloneNativeFactoryBuilder: snapshot.factoryBuilder}
		nativeInput, err := preparer.prepareRcloneNativeProcessInput(
			ctx, snapshot.binding, snapshot.markerKey, snapshot.leaseConfig,
			snapshot.publicationConfig, preparedAt, snapshot.pointDeadline, false, nil,
		)
		if err != nil {
			lazy.err = lifecycleDeleteRuntimeError(err)
			return
		}
		input, err := rcloneNativeLifecycleReconcileInputSnapshot(snapshot, nativeInput)
		if err != nil {
			lazy.err = lifecycleDeleteRuntimeError(err)
			return
		}
		if input.NativeRequest == nil || input.NativeRequest.ClientFactory == nil {
			lazy.err = lifecycleDeletionUnavailable()
			return
		}
		s3, err := input.NativeRequest.ClientFactory.S3(
			input.NativeRequest.Session, input.NativeRequest.Profile, input.NativeRequest.KMSKeyBindings,
		)
		if err != nil {
			lazy.err = lifecycleDeleteRuntimeError(err)
			return
		}
		if s3 == nil {
			lazy.err = lifecycleDeletionUnavailable()
			return
		}
		client, ok := provider.RcloneNativeExactVersionDeleterFromS3(s3)
		if !ok {
			lazy.err = lifecycleDeletionUnavailable()
			return
		}
		lazy.client = client
	})
	if lazy.err != nil {
		return nil, lazy.err
	}
	if lazy.client == nil {
		return nil, lifecycleDeletionUnavailable()
	}
	return lazy.client, nil
}

func (lazy *rcloneNativeLifecycleLazyExactVersionDeleter) ProbeExactVersion(
	ctx context.Context,
	version provider.RcloneNativeExactVersion,
) (provider.RcloneNativeVersionProbe, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := lazy.materialize(ctx)
	if err != nil {
		return provider.RcloneNativeVersionProbe{}, err
	}
	return client.ProbeExactVersion(ctx, version)
}

func (lazy *rcloneNativeLifecycleLazyExactVersionDeleter) DeleteExactVersion(
	ctx context.Context,
	version provider.RcloneNativeExactVersion,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := lazy.materialize(ctx)
	if err != nil {
		return err
	}
	return client.DeleteExactVersion(ctx, version)
}

func rcloneNativeLifecycleReconcileInputSnapshot(
	snapshot rcloneNativeLifecycleReconcileSnapshot,
	nativeInput *managedRcloneNativeProcessInput,
) (provider.RcloneReconcileInput, error) {
	if nativeInput == nil || nativeInput.factory == nil || len(nativeInput.rcloneConfig) == 0 {
		return provider.RcloneReconcileInput{}, fmt.Errorf("%w: native Rclone reconcile input is unavailable", backupasset.ErrCapabilityUnavailable)
	}
	config := snapshot.publicationConfig
	limits := rcloneManifestLimits(config)
	if config.Rclone.ManifestChunkMaxBytes <= 0 || config.Rclone.ManifestChunkMaxBytes > int64(^uint(0)>>1) ||
		config.Rclone.ControlPayloadMaxBytes <= 0 || config.Rclone.FullVerifyMaxBytes <= 0 {
		return provider.RcloneReconcileInput{}, fmt.Errorf("%w: managed Rclone limits are invalid", backupasset.ErrInvalidState)
	}
	chunkMaxEntries := int64(10000)
	if limits.MaxEntries < chunkMaxEntries {
		chunkMaxEntries = limits.MaxEntries
	}
	if chunkMaxEntries <= 0 {
		return provider.RcloneReconcileInput{}, fmt.Errorf("%w: managed Rclone manifest entry limit is invalid", backupasset.ErrInvalidState)
	}
	manifestOptions := provider.RcloneManifestBuildOptions{
		Limits: limits, ChunkMaxBytes: int(config.Rclone.ManifestChunkMaxBytes),
		ChunkMaxEntries: int(chunkMaxEntries), SpoolMaxBytes: limits.MaxBytes,
	}
	return provider.RcloneReconcileInput{
		ManifestLimits: limits,
		NativeRequest: &provider.RcloneNativePublicationRequest{
			Attempt: snapshot.attempt, Profile: nativeInput.profile, Session: nativeInput.session,
			ClientFactory: nativeInput.factory, ManifestOptions: manifestOptions,
			ObservationLimits: nativeInput.observationLimits, Encryption: nativeInput.encryption,
			EncryptionEvidence:       nativeInput.encryptionEvidence,
			KMSKeyBindings:           append([]provider.RcloneNativeKMSKeyDigestBinding(nil), nativeInput.keyBindings...),
			MarkerKey:                append([]byte(nil), snapshot.markerKey...),
			CapabilityEvidenceDigest: snapshot.binding.PreflightDigest,
			CostEvidenceDigest:       digestRclonePublicationCost(config.Rclone),
			MaxVerifyBytes:           0, LowLevelRetries: 0,
			ControlPayloadMaxBytes: uint64(config.Rclone.ControlPayloadMaxBytes),
			ExactCommitKey:         snapshot.commitKey, ExactCommitVersionID: snapshot.commitVersionID,
		},
	}, nil
}

func cloneRcloneAttemptForLifecycle(value provider.RcloneAttemptV1) provider.RcloneAttemptV1 {
	if value.Portable != nil {
		portable := *value.Portable
		if value.Portable.CopyDest != nil {
			copyDest := *value.Portable.CopyDest
			portable.CopyDest = &copyDest
		}
		value.Portable = &portable
	}
	if value.Native != nil {
		native := *value.Native
		value.Native = &native
	}
	return value
}

func cloneManagedRcloneBindingDocumentForLifecycle(value managedRcloneBindingDocumentV3) managedRcloneBindingDocumentV3 {
	if value.Portable != nil {
		portable := *value.Portable
		portable.DependencyRemotes = append([]string(nil), value.Portable.DependencyRemotes...)
		value.Portable = &portable
	}
	if value.Native != nil {
		native := *value.Native
		native.RetainedReadKeys = append([]managedRcloneKMSReadKeyV3(nil), value.Native.RetainedReadKeys...)
		if value.Native.Bootstrap != nil {
			bootstrap := *value.Native.Bootstrap
			if value.Native.Bootstrap.Workload != nil {
				workload := *value.Native.Bootstrap.Workload
				bootstrap.Workload = &workload
			}
			if value.Native.Bootstrap.Static != nil {
				static := *value.Native.Bootstrap.Static
				bootstrap.Static = &static
			}
			native.Bootstrap = &bootstrap
		}
		value.Native = &native
	}
	return value
}
