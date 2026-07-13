package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repositoryRuntime struct {
	repository model.BackupRepository
	binding    model.RepositoryAccessBinding
	document   bindingDocument
	access     provider.AccessBinding
	task       model.Task
}

func (service *Service) Reconcile(ctx context.Context, repositoryID string, requestContext RequestContext) (ConnectResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ConnectResult{}, err
	}
	if err := service.requireRuntime(); err != nil {
		return ConnectResult{}, err
	}
	runtime, err := service.loadRepositoryRuntime(ctx, repositoryID)
	if err != nil {
		return ConnectResult{}, err
	}
	runtime.access = withRemoteAuditContext(runtime.access, requestContext, runtime.document.TaskID)
	prober, err := service.registry.Prober(runtime.access.Provider)
	if err != nil {
		return ConnectResult{}, err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return ConnectResult{}, err
	}
	observation, probeErr := prober.Probe(ctx, runtime.access, limits)
	if probeErr != nil {
		reason := capabilityReasonForProviderError(probeErr)
		if stateErr := service.recordReconcileFailure(context.WithoutCancel(ctx), repositoryID, reason); stateErr != nil {
			return ConnectResult{}, stateErr
		}
		service.writeAudit(context.WithoutCancel(ctx), requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeFailure, repositoryID, &runtime.document.TaskID, "probe", probeErr)
		return ConnectResult{}, probeErr
	}
	if validationErr := validateObservation(runtime.access, observation); validationErr != nil {
		reason := backupasset.CapabilityReason{Code: backupasset.CapabilityProviderProtocolIncompatible}
		if stateErr := service.recordReconcileFailure(context.WithoutCancel(ctx), repositoryID, reason); stateErr != nil {
			return ConnectResult{}, stateErr
		}
		service.writeAudit(context.WithoutCancel(ctx), requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeFailure, repositoryID, &runtime.document.TaskID, "validate", validationErr)
		return ConnectResult{}, validationErr
	}
	if runtime.repository.RepositoryIdentity == nil || *runtime.repository.RepositoryIdentity != observation.RepositoryIdentity {
		return ConnectResult{}, fmt.Errorf("%w: repository identity mismatch", backupasset.ErrConflict)
	}

	var repository model.BackupRepository
	var mutablePoint *model.RecoveryPoint
	runTransaction := func() error {
		repository = model.BackupRepository{}
		mutablePoint = nil
		return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("%w: repository", backupasset.ErrNotFound)
				}
				return fmt.Errorf("reload repository for reconcile: %w", err)
			}
			if repository.RepositoryIdentity == nil || *repository.RepositoryIdentity != observation.RepositoryIdentity || repository.ProviderKind != string(observation.Provider) {
				return fmt.Errorf("%w: repository identity changed during reconcile", backupasset.ErrConflict)
			}
			var binding model.RepositoryAccessBinding
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND repository_id = ? AND status = ?", runtime.binding.ID, repositoryID, bindingStatusActive).First(&binding).Error; err != nil {
				return fmt.Errorf("%w: active repository binding changed", backupasset.ErrConflict)
			}
			document, err := decodeBindingDocument(binding.EncryptedConfig)
			if err != nil {
				return err
			}
			capabilitiesJSON, err := json.Marshal(observation.Capabilities)
			if err != nil {
				return fmt.Errorf("marshal reconciled capabilities: %w", err)
			}
			capabilitiesChanged, err := effectiveCapabilitiesChanged(repository.CapabilitiesJSON, observation.Capabilities)
			if err != nil {
				return err
			}
			if capabilitiesChanged || document.AdapterRevision != observation.AdapterRevision {
				repository.CapabilityRevision++
			}
			if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryOnline); err != nil {
				return err
			}
			now := service.utcNow()
			repository.Status = string(backupasset.RepositoryOnline)
			repository.CapabilitiesJSON = string(capabilitiesJSON)
			repository.LastSeenAt = &now
			repository.LastReconciledAt = &now
			repository.UpdatedAt = now
			if err := tx.Save(&repository).Error; err != nil {
				return fmt.Errorf("update reconciled repository: %w", err)
			}

			document.Backend = observation.InternalProviderFacts["backend"]
			document.RangeProven = observation.Capabilities.OpenRange
			document.AdapterRevision = observation.AdapterRevision
			if observation.Provider == backupasset.ProviderRestic {
				document.NativeRepositoryID = strings.TrimPrefix(observation.RepositoryIdentity, provider.NativeResticIdentityPrefix)
			}
			payload, err := encodeBindingDocument(document)
			if err != nil {
				return err
			}
			fingerprint := observation.ConfigFingerprint
			if !isLowerHex64(fingerprint) {
				salt, err := hexDecodeSalt(document.IdentitySalt)
				if err != nil {
					return err
				}
				fingerprint, err = provider.DeriveConfigFingerprint(salt, []byte(payload))
				if err != nil {
					return err
				}
			}
			binding.EncryptedConfig = payload
			binding.ConfigFingerprint = fingerprint
			binding.UpdatedAt = now
			if err := tx.Save(&binding).Error; err != nil {
				return fmt.Errorf("update reconciled repository binding: %w", err)
			}
			if observation.VersionMode == backupasset.VersionMutableHead {
				point, err := ensureMutablePoint(tx, repository, runtime.task, observation, now)
				if err != nil {
					return err
				}
				mutablePoint = &point
			}
			return nil
		})
	}
	err = runTransaction()
	if isConnectConstraintConflict(err) {
		err = runTransaction()
	}
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeBlocked, repositoryID, &runtime.document.TaskID, "commit", err)
		return ConnectResult{}, err
	}
	result, err := connectResultFromModels(repository, mutablePoint)
	if err != nil {
		return ConnectResult{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryReconcile, backupasset.AuditOutcomeSuccess, repositoryID, &runtime.document.TaskID, "commit", nil)
	return result, nil
}

func (service *Service) Disconnect(ctx context.Context, repositoryID string, requestContext RequestContext) (ConnectResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ConnectResult{}, err
	}
	if service.db == nil || backupasset.ValidateOpaqueID(repositoryID) != nil {
		return ConnectResult{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	var repository model.BackupRepository
	var mutablePoint *model.RecoveryPoint
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: repository", backupasset.ErrNotFound)
			}
			return fmt.Errorf("load repository for disconnect: %w", err)
		}
		now := service.utcNow()
		var binding model.RepositoryAccessBinding
		bindingErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&binding).Error
		if bindingErr == nil {
			if err := tx.Model(&model.RepositoryAccessBinding{}).Where("id = ? AND status = ?", binding.ID, bindingStatusActive).Updates(map[string]any{
				"status": bindingStatusRevoked, "revoked_at": now, "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("revoke repository binding: %w", err)
			}
		} else if !errors.Is(bindingErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load repository binding for disconnect: %w", bindingErr)
		} else if backupasset.RepositoryStatus(repository.Status) != backupasset.RepositoryDisconnected {
			return fmt.Errorf("%w: active repository binding missing", backupasset.ErrConflict)
		}
		if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryDisconnected); err != nil {
			return err
		}
		capabilities, err := capabilitiesWithReason(repository.CapabilitiesJSON, backupasset.CapabilityRepositoryDisconnected)
		if err != nil {
			return err
		}
		repository.Status = string(backupasset.RepositoryDisconnected)
		repository.CapabilitiesJSON = capabilities
		repository.LastReconciledAt = &now
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("update disconnected repository: %w", err)
		}
		var point model.RecoveryPoint
		pointErr := tx.Where("repository_id = ? AND semantics = ?", repositoryID, backupasset.PointMutableHead).First(&point).Error
		if pointErr == nil {
			if err := tx.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
				"physical_availability": string(backupasset.PhysicalOffline), "capabilities_json": capabilities, "updated_at": now,
			}).Error; err != nil {
				return fmt.Errorf("mark disconnected mutable point offline: %w", err)
			}
			point.PhysicalAvailability = string(backupasset.PhysicalOffline)
			point.CapabilitiesJSON = capabilities
			point.UpdatedAt = now
			mutablePoint = &point
		} else if !errors.Is(pointErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("load mutable point for disconnect: %w", pointErr)
		}
		return nil
	})
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryDisconnect, backupasset.AuditOutcomeBlocked, repositoryID, nil, "commit", err)
		return ConnectResult{}, err
	}
	result, err := connectResultFromModels(repository, mutablePoint)
	if err != nil {
		return ConnectResult{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryDisconnect, backupasset.AuditOutcomeSuccess, repositoryID, nil, "commit", nil)
	return result, nil
}

func (service *Service) loadRepositoryRuntime(ctx context.Context, repositoryID string) (repositoryRuntime, error) {
	if backupasset.ValidateOpaqueID(repositoryID) != nil {
		return repositoryRuntime{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", repositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
		}
		return repositoryRuntime{}, fmt.Errorf("load repository runtime: %w", err)
	}
	var binding model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("repository_id = ? AND status = ?", repositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: active repository binding", backupasset.ErrConflict)
		}
		return repositoryRuntime{}, fmt.Errorf("load active repository binding: %w", err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		return repositoryRuntime{}, err
	}
	var taskEntity model.Task
	if err := service.db.WithContext(ctx).Preload("Node.SSHKey").First(&taskEntity, document.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repositoryRuntime{}, fmt.Errorf("%w: binding Task unavailable", backupasset.ErrConflict)
		}
		return repositoryRuntime{}, fmt.Errorf("load binding Task: %w", err)
	}
	if taskEntity.NodeID != document.NodeID || taskEntity.ArchivedAt != nil {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Task lineage changed", backupasset.ErrConflict)
	}
	if bindingProviderForTask(taskEntity) != document.Provider {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Task Provider changed", backupasset.ErrConflict)
	}
	access, err := accessFromBindingDocument(document, taskEntity.Node)
	if err != nil {
		return repositoryRuntime{}, err
	}
	access.RepositoryID = repositoryID
	if string(access.Provider) != repository.ProviderKind {
		return repositoryRuntime{}, fmt.Errorf("%w: binding Provider mismatch", backupasset.ErrConflict)
	}
	return repositoryRuntime{repository: repository, binding: binding, document: document, access: access, task: taskEntity}, nil
}

func (service *Service) providerOperationLimits() (provider.OperationLimits, error) {
	config, err := service.foundation.ProviderConfig()
	if err != nil {
		return provider.OperationLimits{}, err
	}
	return provider.NewMetadataOperationLimits(config.OperationTimeout, config.MetadataLimitBytes)
}

func (service *Service) recordReconcileFailure(ctx context.Context, repositoryID string, reason backupasset.CapabilityReason) error {
	if err := backupasset.ValidateCapabilityReason(reason); err != nil {
		return err
	}
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&repository, "id = ?", repositoryID).Error; err != nil {
			return fmt.Errorf("load repository for failed reconcile: %w", err)
		}
		if err := backupasset.ValidateRepositoryTransition(backupasset.RepositoryStatus(repository.Status), backupasset.RepositoryOffline); err != nil {
			return err
		}
		capabilities, err := capabilitiesWithReason(repository.CapabilitiesJSON, reason.Code)
		if err != nil {
			return err
		}
		now := service.utcNow()
		repository.Status = string(backupasset.RepositoryOffline)
		repository.CapabilitiesJSON = capabilities
		repository.LastReconciledAt = &now
		repository.UpdatedAt = now
		if err := tx.Save(&repository).Error; err != nil {
			return fmt.Errorf("update failed repository observation: %w", err)
		}
		if err := tx.Model(&model.RecoveryPoint{}).
			Where("repository_id = ? AND semantics = ?", repositoryID, backupasset.PointMutableHead).
			Updates(map[string]any{
				"physical_availability": string(backupasset.PhysicalOffline),
				"capabilities_json":     capabilities,
				"updated_at":            now,
			}).Error; err != nil {
			return fmt.Errorf("update failed mutable observation: %w", err)
		}
		return nil
	})
}

func effectiveCapabilitiesChanged(raw string, observed backupasset.CapabilitySet) (bool, error) {
	current, err := decodeRepositoryCapabilities(raw)
	if err != nil {
		return false, err
	}
	current.Reason = nil
	observed.Reason = nil
	return current != observed, nil
}

func capabilitiesWithReason(raw string, code backupasset.CapabilityCode) (string, error) {
	capabilities, err := decodeRepositoryCapabilities(raw)
	if err != nil {
		return "", err
	}
	reason := backupasset.CapabilityReason{Code: code}
	if err := backupasset.ValidateCapabilityReason(reason); err != nil {
		return "", err
	}
	capabilities.Reason = &reason
	payload, err := json.Marshal(capabilities)
	if err != nil {
		return "", fmt.Errorf("marshal repository capability reason: %w", err)
	}
	return string(payload), nil
}

func decodeRepositoryCapabilities(raw string) (backupasset.CapabilitySet, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var capabilities backupasset.CapabilitySet
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return backupasset.CapabilitySet{}, fmt.Errorf("%w: invalid repository capabilities", backupasset.ErrInvalidState)
	}
	if capabilities.Reason != nil {
		if err := backupasset.ValidateCapabilityReason(*capabilities.Reason); err != nil {
			return backupasset.CapabilitySet{}, err
		}
	}
	return capabilities, nil
}

func capabilityReasonForProviderError(err error) backupasset.CapabilityReason {
	var capabilityErr *provider.CapabilityError
	if errors.As(err, &capabilityErr) && backupasset.ValidateCapabilityReason(capabilityErr.Reason) == nil {
		return capabilityErr.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}
	}
	return backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable}
}

func connectResultFromModels(repository model.BackupRepository, mutablePoint *model.RecoveryPoint) (ConnectResult, error) {
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
	return result, nil
}
