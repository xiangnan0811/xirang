package repository

import (
	"context"
	"errors"
	"reflect"

	"xirang/backend/internal/backupasset"

	"gorm.io/gorm"
)

func (service *Service) rcloneCandidateProgress(ctx context.Context, taskID uint, revision uint64) (backupasset.RclonePublicationSummary, bool, error) {
	service.rcloneWorkflowMu.Lock()
	if service.rcloneCandidates == nil || service.rclonePreflights == nil || service.rcloneActivations[taskID] != "" {
		service.rcloneWorkflowMu.Unlock()
		return backupasset.RclonePublicationSummary{}, false, nil
	}
	candidate, ok := service.rcloneCandidates.get(taskID)
	var record rcloneVersioningPreflightRecord
	var ready bool
	if ok {
		record, ready = service.rclonePreflights.get(candidate.latestPreflightID)
	}
	service.rcloneWorkflowMu.Unlock()
	if !ok || candidate.expectedTaskRevision != revision {
		return backupasset.RclonePublicationSummary{}, false, nil
	}
	if err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return revalidateRcloneWorkflowPlanTx(ctx, tx, candidate.plan)
	}); err != nil {
		if errors.Is(err, backupasset.ErrConflict) {
			return backupasset.RclonePublicationSummary{}, false, nil
		}
		return backupasset.RclonePublicationSummary{}, false, err
	}
	var config backupasset.PublicationConfig
	if candidate.settling != nil || ready {
		var err error
		config, err = service.foundation.PublicationConfig()
		if err != nil {
			return backupasset.RclonePublicationSummary{}, false, err
		}
	}
	service.rcloneWorkflowMu.Lock()
	current, currentOK := service.rcloneCandidates.get(taskID)
	currentOK = currentOK && current.setupID == candidate.setupID && current.latestPreflightID == candidate.latestPreflightID &&
		service.rcloneActivations[taskID] == ""
	if ready {
		_, ready = service.rclonePreflights.get(candidate.latestPreflightID)
	}
	service.rcloneWorkflowMu.Unlock()
	if !currentOK {
		return backupasset.RclonePublicationSummary{}, false, nil
	}
	if candidate.settling != nil || ready {
		if candidate.settling != nil && candidate.settlingConfig != nil && reflect.DeepEqual(*candidate.settlingConfig, config) {
			return backupasset.SafeRclonePublicationSummary(rclonePreflightSummary(candidate, *candidate.settling)), true, nil
		}
		if ready && record.candidate.setupID == candidate.setupID && record.id == candidate.latestPreflightID && reflect.DeepEqual(record.publicationConfig, config) {
			return backupasset.SafeRclonePublicationSummary(rclonePreflightSummary(candidate, record.evidence)), true, nil
		}
		return backupasset.RclonePublicationSummary{}, false, nil
	}
	if candidate.purpose == backupasset.RclonePreflightPurposeRenewal {
		return backupasset.RclonePublicationSummary{}, false, nil
	}
	return backupasset.SafeRclonePublicationSummary(rcloneBindingCandidateSummary(candidate)), true, nil
}
