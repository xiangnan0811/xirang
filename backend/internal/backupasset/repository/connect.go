package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	bindingStatusActive  = "active"
	bindingStatusRevoked = "revoked"
)

type ConnectRequest struct {
	TaskID        uint
	RepositoryID  string
	DisplayName   string
	Description   string
	ReplaceAccess bool
}

type ConnectResult struct {
	Repository   backupasset.RepositoryDTO
	MutablePoint *backupasset.RecoveryPointDTO
}

type connectTaskLinkSnapshot struct {
	active *model.TaskRepositoryLink
}

type connectProbeLineage struct {
	task    model.Task
	binding *model.RepositoryAccessBinding
}

// managedRsyncActivationRequest is an internal hand-off between the future
// preflight/migration workflow and the repository boundary. It is deliberately
// not an API request: no caller may synthesize a mode change through Connect.
type managedRsyncActivationRequest struct {
	TaskID                  uint
	ExpectedTaskRevision    uint64
	PreflightID             string
	PreflightIdentityDigest string
	PreflightFenceDigest    string
	Binding                 managedRsyncBindingDocumentV2
}

// managedRsyncActivationPlan contains only internal records needed by the
// later activation transaction. Preparing it is read-only so Task 3 cannot
// create a half-configured versioned task before the preflight service exists.
type managedRsyncActivationPlan struct {
	Task       model.Task
	Repository model.BackupRepository
	Link       model.TaskRepositoryLink
	Binding    managedRsyncBindingDocumentV2
}

func (service *Service) Connect(ctx context.Context, request ConnectRequest, requestContext RequestContext) (ConnectResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ConnectResult{}, err
	}
	if err := service.requireRuntime(); err != nil {
		return ConnectResult{}, err
	}
	if request.TaskID == 0 || (request.RepositoryID != "" && backupasset.ValidateOpaqueID(request.RepositoryID) != nil) {
		return ConnectResult{}, fmt.Errorf("%w: invalid connect request", backupasset.ErrInvalidState)
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Where("archived_at IS NULL").Preload("Node.SSHKey").First(&taskEntity, request.TaskID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ConnectResult{}, fmt.Errorf("%w: Task", backupasset.ErrNotFound)
	} else if err != nil {
		return ConnectResult{}, fmt.Errorf("load Task for repository connect: %w", err)
	}
	linkSnapshot, err := service.connectTaskLinkSnapshot(ctx, taskEntity.ID)
	if err != nil {
		return ConnectResult{}, err
	}

	document, access, probeLineage, retainedAccess, err := service.connectAccess(ctx, taskEntity, request)
	if err != nil {
		return ConnectResult{}, err
	}
	access = withRemoteAuditContext(access, requestContext, document.TaskID)
	prober, err := service.registry.Prober(access.Provider)
	if err != nil {
		return ConnectResult{}, err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return ConnectResult{}, err
	}
	observation, err := prober.Probe(ctx, access, limits)
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryConnect, backupasset.AuditOutcomeFailure, "", &taskEntity.ID, "probe", err)
		return ConnectResult{}, err
	}
	if err := validateObservation(access, observation); err != nil {
		return ConnectResult{}, err
	}
	previousAdapterRevision := document.AdapterRevision
	document.Backend = observation.InternalProviderFacts["backend"]
	document.RangeProven = observation.Capabilities.OpenRange
	document.AdapterRevision = observation.AdapterRevision
	if access.Provider == backupasset.ProviderRestic {
		document.NativeRepositoryID = strings.TrimPrefix(observation.RepositoryIdentity, provider.NativeResticIdentityPrefix)
	}
	bindingPayload, err := encodeBindingDocument(document)
	if err != nil {
		return ConnectResult{}, err
	}
	fingerprint := observation.ConfigFingerprint
	if !isLowerHex64(fingerprint) {
		fingerprint, err = provider.DeriveConfigFingerprint(access.IdentitySalt, []byte(bindingPayload))
		if err != nil {
			return ConnectResult{}, err
		}
	}

	var repository model.BackupRepository
	var mutablePoint *model.RecoveryPoint
	runTransaction := func() error {
		repository = model.BackupRepository{}
		mutablePoint = nil
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			currentTask, err := lockAndRevalidateConnectTask(tx, taskEntity)
			if err != nil {
				return err
			}
			if err := lockAndRevalidateConnectTaskLink(tx, currentTask.ID, linkSnapshot); err != nil {
				return err
			}
			if probeLineage.task.ID != currentTask.ID {
				if _, err := lockAndRevalidateConnectTask(tx, probeLineage.task); err != nil {
					return err
				}
			}
			if probeLineage.binding != nil {
				if err := lockAndRevalidateRetainedProbeBinding(tx, *probeLineage.binding, observation); err != nil {
					return err
				}
			}
			resolved, created, err := service.resolveRepositoryForConnect(tx, request, currentTask, observation)
			if err != nil {
				return err
			}
			repository = resolved
			capabilitiesJSON, err := json.Marshal(observation.Capabilities)
			if err != nil {
				return fmt.Errorf("marshal repository capabilities: %w", err)
			}
			now := service.utcNow()
			if created {
				repository.CapabilitiesJSON = string(capabilitiesJSON)
				repository.LastSeenAt = &now
				repository.LastReconciledAt = &now
				if err := tx.Create(&repository).Error; err != nil {
					return fmt.Errorf("create backup repository: %w", err)
				}
			} else {
				boundIdentity, bindErr := backupasset.BindImportedRepositoryIdentity(repository.RepositoryIdentity, observation.RepositoryIdentity)
				if bindErr != nil || repository.ProviderKind != string(observation.Provider) {
					return fmt.Errorf("%w: repository identity mismatch", backupasset.ErrConflict)
				}
				repository.RepositoryIdentity = &boundIdentity
				if repository.CapabilitiesJSON != string(capabilitiesJSON) || (retainedAccess && previousAdapterRevision != observation.AdapterRevision) {
					repository.CapabilityRevision++
				}
				if backupasset.RepositoryStatus(repository.Status) == backupasset.RepositoryDisconnected {
					if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryDisconnected, backupasset.RepositoryConnecting); err != nil {
						return err
					}
					repository.Status = string(backupasset.RepositoryConnecting)
					if err := tx.Save(&repository).Error; err != nil {
						return err
					}
				}
				if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryOnline); err != nil {
					return err
				}
				repository.Status = string(backupasset.RepositoryOnline)
				repository.CapabilitiesJSON = string(capabilitiesJSON)
				repository.LastSeenAt = &now
				repository.LastReconciledAt = &now
				repository.UpdatedAt = now
				if err := tx.Save(&repository).Error; err != nil {
					return fmt.Errorf("update backup repository: %w", err)
				}
			}
			if err := ensureLegacyTaskLink(tx, repository, currentTask, document, now); err != nil {
				return err
			}
			if err := ensureAccessBinding(tx, repository.ID, bindingPayload, fingerprint, request, retainedAccess, now); err != nil {
				return err
			}
			if observation.VersionMode == backupasset.VersionMutableHead {
				point, err := ensureMutablePoint(tx, repository, currentTask, observation, now)
				if err != nil {
					return err
				}
				mutablePoint = point
			}
			return nil
		})
	}
	err = runTransaction()
	if isConnectConstraintConflict(err) {
		err = runTransaction()
	}
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryConnect, backupasset.AuditOutcomeBlocked, "", &taskEntity.ID, "commit", err)
		return ConnectResult{}, err
	}
	repositoryDTO, err := backupasset.ToRepositoryDTO(repository)
	if err != nil {
		return ConnectResult{}, err
	}
	result := ConnectResult{Repository: repositoryDTO}
	if mutablePoint != nil {
		pointDTO, err := backupasset.ToRecoveryPointDTO(*mutablePoint, backupasset.VersionMode(repository.VersionMode))
		if err != nil {
			return ConnectResult{}, err
		}
		result.MutablePoint = &pointDTO
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryConnect, backupasset.AuditOutcomeSuccess, repository.ID, &taskEntity.ID, "commit", nil)
	return result, nil
}

func (service *Service) connectTaskLinkSnapshot(ctx context.Context, taskID uint) (connectTaskLinkSnapshot, error) {
	var link model.TaskRepositoryLink
	err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return connectTaskLinkSnapshot{}, nil
	}
	if err != nil {
		return connectTaskLinkSnapshot{}, fmt.Errorf("load Task link lineage for repository connect: %w", err)
	}
	return connectTaskLinkSnapshot{active: &link}, nil
}

func lockAndRevalidateConnectTask(tx *gorm.DB, snapshot model.Task) (model.Task, error) {
	var current model.Task
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, snapshot.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Task{}, fmt.Errorf("%w: Task lineage changed during Provider probe", backupasset.ErrConflict)
		}
		return model.Task{}, fmt.Errorf("reload Task after Provider probe: %w", err)
	}
	if current.ArchivedAt != nil || current.NodeID != snapshot.NodeID || bindingProviderForTask(current) != bindingProviderForTask(snapshot) ||
		current.RsyncTarget != snapshot.RsyncTarget || current.ExecutorConfig != snapshot.ExecutorConfig || !current.UpdatedAt.Equal(snapshot.UpdatedAt) {
		return model.Task{}, fmt.Errorf("%w: Task lineage changed during Provider probe", backupasset.ErrConflict)
	}

	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current.Node, current.NodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Task{}, fmt.Errorf("%w: Task Node lineage changed during Provider probe", backupasset.ErrConflict)
		}
		return model.Task{}, fmt.Errorf("reload Task Node after Provider probe: %w", err)
	}
	if !sameConnectNodeLineage(snapshot.Node, current.Node) {
		return model.Task{}, fmt.Errorf("%w: Task Node lineage changed during Provider probe", backupasset.ErrConflict)
	}
	if current.Node.SSHKeyID != nil {
		var key model.SSHKey
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&key, *current.Node.SSHKeyID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.Task{}, fmt.Errorf("%w: Task Node credential lineage changed during Provider probe", backupasset.ErrConflict)
			}
			return model.Task{}, fmt.Errorf("reload Task Node credential after Provider probe: %w", err)
		}
		current.Node.SSHKey = &key
		if snapshot.Node.SSHKey == nil || !sameConnectSSHCredentialLineage(*snapshot.Node.SSHKey, key) {
			return model.Task{}, fmt.Errorf("%w: Task Node credential lineage changed during Provider probe", backupasset.ErrConflict)
		}
	}
	return current, nil
}

func sameConnectNodeLineage(snapshot, current model.Node) bool {
	if snapshot.ID != current.ID || snapshot.Host != current.Host || snapshot.Port != current.Port || snapshot.Username != current.Username ||
		snapshot.AuthType != current.AuthType || snapshot.Password != current.Password || snapshot.PrivateKey != current.PrivateKey ||
		snapshot.Archived != current.Archived || !snapshot.UpdatedAt.Equal(current.UpdatedAt) {
		return false
	}
	if snapshot.SSHKeyID == nil || current.SSHKeyID == nil {
		return snapshot.SSHKeyID == nil && current.SSHKeyID == nil
	}
	return *snapshot.SSHKeyID == *current.SSHKeyID
}

func sameConnectSSHCredentialLineage(snapshot, current model.SSHKey) bool {
	if snapshot.ID != current.ID || snapshot.Username != current.Username || snapshot.KeyType != current.KeyType ||
		snapshot.PrivateKey != current.PrivateKey || snapshot.Fingerprint != current.Fingerprint || snapshot.Disabled != current.Disabled ||
		snapshot.AllowedPurposes != current.AllowedPurposes || snapshot.AllowedNodeIDs != current.AllowedNodeIDs ||
		snapshot.AllowedNodeTags != current.AllowedNodeTags {
		return false
	}
	if snapshot.ExpiresAt == nil || current.ExpiresAt == nil {
		return snapshot.ExpiresAt == nil && current.ExpiresAt == nil
	}
	return snapshot.ExpiresAt.Equal(*current.ExpiresAt)
}

func lockAndRevalidateRetainedProbeBinding(
	tx *gorm.DB,
	snapshot model.RepositoryAccessBinding,
	observation provider.RepositoryObservation,
) error {
	var repository model.BackupRepository
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", snapshot.RepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: retained probe repository changed during Provider probe", backupasset.ErrConflict)
		}
		return fmt.Errorf("reload retained probe repository after Provider probe: %w", err)
	}
	if repository.RepositoryIdentity == nil || *repository.RepositoryIdentity != observation.RepositoryIdentity ||
		repository.ProviderKind != string(observation.Provider) {
		return fmt.Errorf("%w: retained probe repository changed during Provider probe", backupasset.ErrConflict)
	}

	var current model.RepositoryAccessBinding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", snapshot.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: retained probe binding changed during Provider probe", backupasset.ErrConflict)
		}
		return fmt.Errorf("reload retained probe binding after Provider probe: %w", err)
	}
	if snapshot.Status != bindingStatusActive || current.RepositoryID != snapshot.RepositoryID || current.BindingKind != snapshot.BindingKind ||
		current.Status != snapshot.Status || current.EncryptedConfig != snapshot.EncryptedConfig || current.ConfigFingerprint != snapshot.ConfigFingerprint ||
		!sameConnectOptionalTime(current.RevokedAt, snapshot.RevokedAt) || !current.CreatedAt.Equal(snapshot.CreatedAt) ||
		!current.UpdatedAt.Equal(snapshot.UpdatedAt) {
		return fmt.Errorf("%w: retained probe binding changed during Provider probe", backupasset.ErrConflict)
	}
	return nil
}

func sameConnectOptionalTime(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}

func lockAndRevalidateConnectTaskLink(tx *gorm.DB, taskID uint, snapshot connectTaskLinkSnapshot) error {
	if snapshot.active == nil {
		var current model.TaskRepositoryLink
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reload Task link after Provider probe: %w", err)
		}
		return fmt.Errorf("%w: Task link lineage changed during Provider probe", backupasset.ErrConflict)
	}

	var current model.TaskRepositoryLink
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", snapshot.active.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: Task link lineage changed during Provider probe", backupasset.ErrConflict)
		}
		return fmt.Errorf("reload Task link after Provider probe: %w", err)
	}
	if current.TaskID == nil || *current.TaskID != taskID || current.UnlinkedAt != nil || current.RepositoryID != snapshot.active.RepositoryID ||
		current.PublicationMode != snapshot.active.PublicationMode || current.EncryptedLegacyLocator != snapshot.active.EncryptedLegacyLocator ||
		!current.UpdatedAt.Equal(snapshot.active.UpdatedAt) {
		return fmt.Errorf("%w: Task link lineage changed during Provider probe", backupasset.ErrConflict)
	}
	return nil
}

func (service *Service) prepareManagedRsyncActivation(ctx context.Context, request managedRsyncActivationRequest) (managedRsyncActivationPlan, error) {
	if service == nil || service.db == nil {
		return managedRsyncActivationPlan{}, fmt.Errorf("%w: managed Rsync activation dependencies are unavailable", backupasset.ErrInvalidState)
	}
	if err := validateManagedRsyncActivationRequest(request); err != nil {
		return managedRsyncActivationPlan{}, err
	}
	var plan managedRsyncActivationPlan
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		if err := tx.Where("id = ? AND archived_at IS NULL", request.TaskID).First(&taskEntity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: activation Task", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load managed Rsync activation Task: %w", err)
		}
		revision, err := managedRsyncTaskRevision(taskEntity)
		if err != nil {
			return err
		}
		if revision != request.ExpectedTaskRevision {
			return fmt.Errorf("%w: managed Rsync Task revision changed", backupasset.ErrConflict)
		}

		var link model.TaskRepositoryLink
		if err := tx.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rsync activation link", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load managed Rsync activation link: %w", err)
		}
		if link.PublicationMode != string(backupasset.PublicationLegacyMutable) || strings.TrimSpace(link.EncryptedLegacyLocator) == "" {
			return fmt.Errorf("%w: managed Rsync activation requires an exact legacy link", backupasset.ErrConflict)
		}
		if link.TaskID == nil || *link.TaskID != taskEntity.ID || link.ID != request.Binding.TaskRepositoryLinkID || link.RepositoryID != request.Binding.RepositoryID {
			return fmt.Errorf("%w: managed Rsync activation link identity changed", backupasset.ErrConflict)
		}

		var repository model.BackupRepository
		if err := tx.First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: managed Rsync activation repository", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load managed Rsync activation repository: %w", err)
		}
		if repository.ProviderKind != string(backupasset.ProviderRsync) || repository.VersionMode != string(backupasset.VersionMutableHead) {
			return fmt.Errorf("%w: managed Rsync activation repository is not legacy mutable", backupasset.ErrConflict)
		}
		if request.Binding.TaskID != taskEntity.ID || request.Binding.NodeID != taskEntity.NodeID ||
			filepath.Clean(request.Binding.ManagedRootLocator) == filepath.Clean(taskEntity.RsyncTarget) {
			return fmt.Errorf("%w: managed Rsync activation Task identity changed", backupasset.ErrConflict)
		}
		plan = managedRsyncActivationPlan{Task: taskEntity, Repository: repository, Link: link, Binding: request.Binding}
		return nil
	})
	if err != nil {
		return managedRsyncActivationPlan{}, err
	}
	return plan, nil
}

func validateManagedRsyncActivationRequest(request managedRsyncActivationRequest) error {
	if request.TaskID == 0 || request.ExpectedTaskRevision == 0 || backupasset.ValidateOpaqueID(request.PreflightID) != nil ||
		!isLowerHex64(request.PreflightIdentityDigest) || !isLowerHex64(request.PreflightFenceDigest) {
		return fmt.Errorf("%w: invalid managed Rsync activation proof", backupasset.ErrConflict)
	}
	if err := validateManagedRsyncBindingDocumentV2(request.Binding); err != nil {
		return err
	}
	if request.Binding.TaskID != request.TaskID || request.Binding.RootMarkerDigest != request.PreflightIdentityDigest {
		return fmt.Errorf("%w: managed Rsync preflight identity changed", backupasset.ErrConflict)
	}
	return nil
}

func managedRsyncTaskRevision(taskEntity model.Task) (uint64, error) {
	if taskEntity.ID == 0 || taskEntity.UpdatedAt.IsZero() {
		return 0, fmt.Errorf("%w: managed Rsync Task revision unavailable", backupasset.ErrInvalidState)
	}
	nanos := taskEntity.UpdatedAt.UTC().UnixNano()
	if nanos <= 0 {
		return 0, fmt.Errorf("%w: managed Rsync Task revision unavailable", backupasset.ErrInvalidState)
	}
	return uint64(nanos), nil
}

func (service *Service) connectAccess(ctx context.Context, taskEntity model.Task, request ConnectRequest) (bindingDocument, provider.AccessBinding, connectProbeLineage, bool, error) {
	if !request.ReplaceAccess {
		var link model.TaskRepositoryLink
		err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error
		if err == nil {
			if request.RepositoryID != "" && request.RepositoryID != link.RepositoryID {
				return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: Task already linked to another repository", backupasset.ErrConflict)
			}
			var binding model.RepositoryAccessBinding
			bindingErr := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error
			switch {
			case bindingErr == nil:
				stored, decodeErr := decodeStoredBindingDocument(binding.EncryptedConfig)
				if decodeErr != nil {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, decodeErr
				}
				if stored.ManagedRsyncV2 != nil {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: managed Rsync binding requires explicit activation", backupasset.ErrConflict)
				}
				if stored.V1 == nil {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: unsupported retained binding document", backupasset.ErrInvalidState)
				}
				document := *stored.V1
				currentProvider := bindingProviderForTask(taskEntity)
				if currentProvider != document.Provider {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: Task Provider changed", backupasset.ErrConflict)
				}
				if document.IdentityClass == provider.IdentityTaskScopedEndpoint {
					salt, saltErr := hexDecodeSalt(document.IdentitySalt)
					if saltErr != nil {
						return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, saltErr
					}
					currentDocument, _, currentErr := bindingFromTask(taskEntity, taskEntity.Node, salt)
					if currentErr != nil || currentDocument.Provider != document.Provider || currentDocument.Locator != document.Locator ||
						!slices.Equal(currentDocument.EndpointFacts, document.EndpointFacts) {
						return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: Task repository identity changed", backupasset.ErrConflict)
					}
				}
				var bindingTask model.Task
				if loadErr := service.db.WithContext(ctx).Where("archived_at IS NULL").Preload("Node.SSHKey").First(&bindingTask, document.TaskID).Error; loadErr != nil {
					if errors.Is(loadErr, gorm.ErrRecordNotFound) {
						return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: retained binding Task unavailable", backupasset.ErrConflict)
					}
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("load retained binding Task: %w", loadErr)
				}
				if bindingTask.NodeID != document.NodeID || bindingProviderForTask(bindingTask) != document.Provider {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("%w: retained binding lineage changed", backupasset.ErrConflict)
				}
				access, accessErr := accessFromBindingDocument(document, bindingTask.Node)
				if accessErr != nil {
					return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, accessErr
				}
				access.RepositoryID = link.RepositoryID
				return document, access, connectProbeLineage{task: bindingTask, binding: &binding}, true, nil
			case !errors.Is(bindingErr, gorm.ErrRecordNotFound):
				return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("load retained repository binding: %w", bindingErr)
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, fmt.Errorf("load active Task repository link: %w", err)
		}
	}

	salt, err := service.bindingSalt(ctx, taskEntity.ID, request.RepositoryID)
	if err != nil {
		return bindingDocument{}, provider.AccessBinding{}, connectProbeLineage{}, false, err
	}
	document, access, err := bindingFromTask(taskEntity, taskEntity.Node, salt)
	return document, access, connectProbeLineage{task: taskEntity}, false, err
}

func (service *Service) bindingSalt(ctx context.Context, taskID uint, repositoryID string) ([]byte, error) {
	var link model.TaskRepositoryLink
	err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).First(&link).Error
	if err == nil {
		return service.saltForRepository(ctx, link.RepositoryID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("load active Task repository link: %w", err)
	}
	if repositoryID != "" {
		return service.saltForRepository(ctx, repositoryID)
	}
	return generateBindingSalt()
}

func (service *Service) saltForRepository(ctx context.Context, repositoryID string) ([]byte, error) {
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ?", repositoryID).Order("created_at DESC").First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return generateBindingSalt()
		}
		return nil, fmt.Errorf("load repository binding salt: %w", err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return nil, err
	}
	return hexDecodeSalt(document.IdentitySalt)
}

func hexDecodeSalt(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != provider.IdentitySaltBytes {
		return nil, fmt.Errorf("%w: invalid persisted identity salt", backupasset.ErrInvalidState)
	}
	return decoded, nil
}

func (service *Service) resolveRepositoryForConnect(tx *gorm.DB, request ConnectRequest, taskEntity model.Task, observation provider.RepositoryObservation) (model.BackupRepository, bool, error) {
	var activeLink model.TaskRepositoryLink
	linkErr := tx.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&activeLink).Error
	if linkErr != nil && !errors.Is(linkErr, gorm.ErrRecordNotFound) {
		return model.BackupRepository{}, false, linkErr
	}
	requestedID := request.RepositoryID
	if activeLink.ID != "" {
		if requestedID != "" && requestedID != activeLink.RepositoryID {
			return model.BackupRepository{}, false, fmt.Errorf("%w: Task already linked to another repository", backupasset.ErrConflict)
		}
		requestedID = activeLink.RepositoryID
	}
	var repository model.BackupRepository
	var err error
	if requestedID != "" {
		err = tx.First(&repository, "id = ?", requestedID).Error
	} else {
		err = tx.Where("provider_kind = ? AND repository_identity = ?", observation.Provider, observation.RepositoryIdentity).First(&repository).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			placeholder := backupasset.FormatImportedRepositoryIdentity(backupasset.ImportedIdentityRef(observation.RepositoryIdentity))
			err = tx.Where("provider_kind = ? AND repository_identity = ?", observation.Provider, placeholder).First(&repository).Error
		}
	}
	if err == nil {
		return repository, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.BackupRepository{}, false, err
	}
	if requestedID != "" {
		return model.BackupRepository{}, false, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupRepository{}, false, err
	}
	identity := observation.RepositoryIdentity
	now := service.utcNow()
	immutability := backupasset.ImmutabilityMutable
	if observation.Provider == backupasset.ProviderRestic {
		immutability = backupasset.ImmutabilityBackendVersioned
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = taskEntity.Name
	}
	return model.BackupRepository{
		ID: id, ProviderKind: string(observation.Provider), RepositoryIdentity: &identity, DisplayName: displayName,
		Description: strings.TrimSpace(request.Description), VersionMode: string(observation.VersionMode), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, ImmutabilityLevel: string(immutability), CreatedAt: now, UpdatedAt: now,
	}, true, nil
}

// ensureLegacyTaskLink only creates the V1-derived mutable/native link shape.
// Managed Rsync migration must use its dedicated activation transaction and
// must never silently reinterpret this link as a versioned tree.
func ensureLegacyTaskLink(tx *gorm.DB, repository model.BackupRepository, taskEntity model.Task, document bindingDocument, now time.Time) error {
	if err := validateBindingDocument(document); err != nil {
		return err
	}
	var link model.TaskRepositoryLink
	err := tx.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error
	if err == nil {
		if link.RepositoryID != repository.ID {
			return fmt.Errorf("%w: active Task link identity mismatch", backupasset.ErrConflict)
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	taskID := taskEntity.ID
	publicationMode := backupasset.PublicationLegacyMutable
	if document.Provider == backupasset.ProviderRestic {
		publicationMode = backupasset.PublicationNativeSnapshot
	}
	link = model.TaskRepositoryLink{
		ID: id, TaskID: &taskID, RepositoryID: repository.ID, TaskNameSnapshot: taskEntity.Name,
		NodeIDSnapshot: taskEntity.NodeID, NodeNameSnapshot: taskEntity.Node.Name, PublicationMode: string(publicationMode),
		EncryptedLegacyLocator: document.Locator, LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&link).Error; err != nil {
		return fmt.Errorf("create Task repository link: %w", err)
	}
	return nil
}

func ensureAccessBinding(tx *gorm.DB, repositoryID, payload, fingerprint string, request ConnectRequest, refreshRetained bool, now time.Time) error {
	var active model.RepositoryAccessBinding
	err := tx.Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&active).Error
	if err == nil {
		if !request.ReplaceAccess {
			if err := ensureRetainedBindingTaskAvailable(tx, active); err != nil {
				return err
			}
			if refreshRetained {
				active.EncryptedConfig = payload
				active.ConfigFingerprint = fingerprint
				active.UpdatedAt = now
				if err := tx.Save(&active).Error; err != nil {
					return fmt.Errorf("refresh retained repository access binding: %w", err)
				}
			}
			return nil
		}
		if request.RepositoryID != repositoryID {
			return fmt.Errorf("%w: replacement requires exact repository target", backupasset.ErrConflict)
		}
		active.Status = bindingStatusRevoked
		active.RevokedAt = &now
		active.UpdatedAt = now
		if err := tx.Select("status", "revoked_at", "updated_at").Save(&active).Error; err != nil {
			return fmt.Errorf("revoke repository access binding: %w", err)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	binding := model.RepositoryAccessBinding{ID: id, RepositoryID: repositoryID, BindingKind: "task_derived_v1", EncryptedConfig: payload, ConfigFingerprint: fingerprint, Status: bindingStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&binding).Error; err != nil {
		return fmt.Errorf("create repository access binding: %w", err)
	}
	return nil
}

func ensureRetainedBindingTaskAvailable(tx *gorm.DB, binding model.RepositoryAccessBinding) error {
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return err
	}
	var taskEntity model.Task
	if err := tx.Select("id", "node_id", "executor_type").Where("id = ? AND archived_at IS NULL", document.TaskID).First(&taskEntity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: retained binding Task unavailable", backupasset.ErrConflict)
		}
		return fmt.Errorf("load retained binding Task: %w", err)
	}
	currentProvider := bindingProviderForTask(taskEntity)
	if currentProvider != document.Provider {
		return fmt.Errorf("%w: retained binding Task Provider changed", backupasset.ErrConflict)
	}
	if taskEntity.NodeID != document.NodeID {
		return fmt.Errorf("%w: retained binding Task Node changed", backupasset.ErrConflict)
	}
	return nil
}

func ensureMutablePoint(tx *gorm.DB, repository model.BackupRepository, taskEntity model.Task, observation provider.RepositoryObservation, now time.Time) (*model.RecoveryPoint, error) {
	var point model.RecoveryPoint
	err := tx.Where("repository_id = ? AND semantics = ?", repository.ID, backupasset.PointMutableHead).First(&point).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		switch backupasset.RecoveryPointState(point.State) {
		case backupasset.RecoveryPointObserved:
		case backupasset.RecoveryPointRetired:
			return nil, nil
		default:
			return nil, fmt.Errorf("%w: mutable point cannot be reactivated", backupasset.ErrConflict)
		}
	}
	capabilitiesJSON, _ := json.Marshal(observation.Capabilities)
	lineageJSON, _ := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: &taskEntity.ID})
	providerLocator, _ := json.Marshal(map[string]string{"kind": "mutable_head", "source_revision": observation.SourceRevision})
	if point.ID == "" {
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return nil, err
		}
		point = model.RecoveryPoint{ID: id, RepositoryID: repository.ID, CreatedAt: now}
	}
	point.ProducingTaskID = &taskEntity.ID
	point.ProducingTaskNameSnapshot = taskEntity.Name
	point.ProducingNodeIDSnapshot = taskEntity.NodeID
	point.ProducingNodeNameSnapshot = taskEntity.Node.Name
	point.LineageJSON = string(lineageJSON)
	point.EncryptedProviderLocator = string(providerLocator)
	point.Semantics = string(backupasset.PointMutableHead)
	point.State = string(backupasset.RecoveryPointObserved)
	observedAt := observation.ObservedAt.UTC()
	if observation.ObservedAt.IsZero() {
		observedAt = now
	}
	point.ObservedAt = &observedAt
	point.SourceFingerprint = observation.SourceRevision
	point.ConsistencyJSON = `{"mode":"mutable"}`
	point.FidelityJSON = `{"strength":"weak"}`
	point.CapabilityRevision = repository.CapabilityRevision
	point.CapabilitiesJSON = string(capabilitiesJSON)
	point.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
	point.PhysicalAvailability = string(observation.Availability)
	point.HoldState = string(backupasset.HoldNone)
	point.UpdatedAt = now
	if err == nil {
		if saveErr := tx.Save(&point).Error; saveErr != nil {
			return nil, saveErr
		}
	} else if createErr := tx.Create(&point).Error; createErr != nil {
		return nil, createErr
	}
	return &point, nil
}

func validateObservation(access provider.AccessBinding, observation provider.RepositoryObservation) error {
	if observation.Provider != access.Provider || strings.TrimSpace(observation.RepositoryIdentity) == "" || observation.VersionMode == "" || observation.AdapterRevision == "" || observation.SourceRevision == "" || observation.Availability != backupasset.PhysicalOnline {
		return fmt.Errorf("%w: invalid Provider observation", backupasset.ErrInvalidState)
	}
	switch access.Provider {
	case backupasset.ProviderRsync, backupasset.ProviderRclone:
		if observation.IdentityClass != provider.IdentityTaskScopedEndpoint || observation.VersionMode != backupasset.VersionMutableHead {
			return fmt.Errorf("%w: invalid scoped Provider observation", backupasset.ErrInvalidState)
		}
		identityFacts := append([]string(nil), access.EndpointFacts...)
		if access.Provider == backupasset.ProviderRclone {
			backend := strings.TrimSpace(observation.InternalProviderFacts["backend"])
			if backend == "" {
				return fmt.Errorf("%w: Rclone backend identity fact unavailable", backupasset.ErrInvalidState)
			}
			identityFacts = append(identityFacts, "backend:"+backend)
		}
		expected, err := provider.DeriveScopedIdentity(access.IdentitySalt, provider.ScopedIdentityDocument{
			Provider: access.Provider, TaskID: access.TaskID, NodeID: access.NodeID, EndpointFacts: identityFacts,
		})
		if err != nil || observation.RepositoryIdentity != expected {
			return fmt.Errorf("%w: scoped repository identity mismatch", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRestic:
		if observation.IdentityClass != provider.IdentityNativeRepository || observation.VersionMode != backupasset.VersionNativeSnapshot {
			return fmt.Errorf("%w: invalid native Provider observation", backupasset.ErrInvalidState)
		}
		nativeID := strings.TrimPrefix(observation.RepositoryIdentity, provider.NativeResticIdentityPrefix)
		expected, err := provider.NativeRepositoryIdentity(backupasset.ProviderRestic, nativeID)
		if err != nil || observation.RepositoryIdentity != expected {
			return fmt.Errorf("%w: invalid native repository identity", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported Provider observation", backupasset.ErrCapabilityUnavailable)
	}
	if observation.Capabilities.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*observation.Capabilities.Reason); err != nil {
			return err
		}
	}
	return nil
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isConnectConstraintConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique constraint") && !strings.Contains(message, "duplicate key") && !strings.Contains(message, "sqlstate 23505") {
		return false
	}
	for _, signature := range []string{
		"idx_backup_repositories_provider_identity",
		"backup_repositories.provider_kind, backup_repositories.repository_identity",
		"idx_repository_access_bindings_active",
		"repository_access_bindings.repository_id",
		"idx_task_repository_links_active_task",
		"task_repository_links.task_id",
		"idx_recovery_points_mutable_head",
		"recovery_points.repository_id",
	} {
		if strings.Contains(message, signature) {
			return true
		}
	}
	return false
}
