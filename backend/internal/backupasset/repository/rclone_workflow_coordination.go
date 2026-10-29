package repository

import (
	"fmt"

	"xirang/backend/internal/backupasset"
)

// All workflow transitions take rcloneWorkflowMu before entering either store.
// The lock is never held during a database operation, provider call, or drain.
func (service *Service) replaceRcloneCandidate(candidate rcloneBindingCandidate) error {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	if service.rcloneActivations[candidate.taskID] != "" {
		return fmt.Errorf("%w: Rclone activation is in progress", backupasset.ErrConflict)
	}
	if err := service.rcloneCandidates.put(candidate); err != nil {
		return err
	}
	service.rclonePreflights.removeForTask(candidate.taskID)
	return nil
}

func (store *rcloneVersioningPreflightStore) removeForTask(taskID uint) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, record := range store.records {
		if record.candidate.taskID == taskID {
			delete(store.records, id)
		}
	}
}

func (service *Service) beginRclonePreflight(taskID uint, revision uint64, mode backupasset.TaskPublicationMode, id string) (rcloneBindingCandidate, error) {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	if service.rcloneActivations[taskID] != "" {
		return rcloneBindingCandidate{}, fmt.Errorf("%w: Rclone activation is in progress", backupasset.ErrConflict)
	}
	candidate, ok := service.rcloneCandidates.get(taskID)
	if !ok || candidate.expectedTaskRevision != revision ||
		(candidate.kind == rcloneBindingSetupPortable) != (mode == backupasset.PublicationVersionedPrefix) || candidate.preflighting {
		return rcloneBindingCandidate{}, fmt.Errorf("%w: Rclone candidate unavailable or preflight in progress", backupasset.ErrConflict)
	}
	candidate.preflighting = true
	candidate.latestPreflightID = id
	candidate.settling = nil
	candidate.settlingConfig = nil
	if err := service.rcloneCandidates.put(candidate); err != nil {
		return rcloneBindingCandidate{}, err
	}
	service.rclonePreflights.removeForTask(taskID)
	return candidate, nil
}

func (service *Service) endRclonePreflight(candidate rcloneBindingCandidate) {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	current, ok := service.rcloneCandidates.get(candidate.taskID)
	if ok && current.setupID == candidate.setupID && current.latestPreflightID == candidate.latestPreflightID {
		current.preflighting = false
		_ = service.rcloneCandidates.put(current)
	}
}

func (service *Service) finalizeRclonePreflight(candidate rcloneBindingCandidate, record rcloneVersioningPreflightRecord) error {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	current, ok := service.rcloneCandidates.get(candidate.taskID)
	if !ok || current.setupID != candidate.setupID || current.latestPreflightID != record.id ||
		!current.preflighting || service.rcloneActivations[candidate.taskID] != "" || !record.expiresAt.After(service.utcNow()) {
		return fmt.Errorf("%w: Rclone preflight candidate changed or expired", backupasset.ErrConflict)
	}
	if candidate.kind == rcloneBindingSetupNative {
		current.nativeObserved = true
		if err := service.rcloneCandidates.put(current); err != nil {
			return err
		}
	}
	if record.evidence.Settling {
		evidence := record.evidence
		config := record.publicationConfig
		current.settling = &evidence
		current.settlingConfig = &config
		return service.rcloneCandidates.put(current)
	}
	return service.rclonePreflights.put(record)
}

func (service *Service) reserveRcloneActivation(request backupasset.RcloneVersioningActivationRequest) (rcloneVersioningPreflightRecord, error) {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	candidate, ok := service.rcloneCandidates.get(request.TaskID)
	if !ok || candidate.preflighting || candidate.latestPreflightID != request.PreflightID || service.rcloneActivations[request.TaskID] != "" {
		return rcloneVersioningPreflightRecord{}, fmt.Errorf("%w: Rclone preflight expired or unavailable", backupasset.ErrConflict)
	}
	record, ok := service.rclonePreflights.consume(request.PreflightID, request.TaskID, request.ExpectedTaskRevision)
	if !ok || record.candidate.setupID != candidate.setupID {
		return rcloneVersioningPreflightRecord{}, fmt.Errorf("%w: Rclone preflight expired or unavailable", backupasset.ErrConflict)
	}
	if service.rcloneActivations == nil {
		service.rcloneActivations = make(map[uint]string)
	}
	service.rcloneActivations[request.TaskID] = candidate.setupID
	return record, nil
}

func (service *Service) releaseRcloneActivation(taskID uint, generation string, committed bool) {
	service.rcloneWorkflowMu.Lock()
	defer service.rcloneWorkflowMu.Unlock()
	if service.rcloneActivations[taskID] != generation {
		return
	}
	delete(service.rcloneActivations, taskID)
	if committed {
		if current, ok := service.rcloneCandidates.get(taskID); ok && current.setupID == generation {
			service.rcloneCandidates.delete(taskID)
		}
	}
}
