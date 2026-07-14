package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

const (
	taskRunCodeInterruptedAfterProviderCommit  = "process_interrupted_after_provider_commit"
	taskRunCodeInterruptedBeforeProviderCommit = "process_interrupted_before_provider_commit"
)

// ReportInterruptedPublication reconciles only a stale TaskRun after the
// worker discovers a durable terminal/verifying publication fact. A current
// process entry is authoritative: post-hooks and normal run finalization win
// over a fast worker callback.
func (manager *Manager) ReportInterruptedPublication(ctx context.Context, outcome publication.Outcome) error {
	if manager == nil || manager.db == nil {
		return fmt.Errorf("%w: interrupted publication reporter unavailable", backupasset.ErrInvalidState)
	}
	if _, live := manager.pendingRuns.Load(outcome.TaskID); live {
		return nil
	}
	status, code, reportable := interruptedPublicationTaskRunState(outcome)
	if !reportable {
		return nil
	}
	return manager.reportInterruptedPublication(ctx, outcome.TaskID, outcome.TaskRunID, status, code)
}

func interruptedPublicationTaskRunState(outcome publication.Outcome) (string, string, bool) {
	if outcome.TaskID == 0 || outcome.TaskRunID == 0 {
		return "", "", false
	}
	if outcome.ProviderCommitRecorded {
		switch outcome.State {
		case backupasset.RecoveryPointVerifying, backupasset.RecoveryPointCommitted, backupasset.RecoveryPointFailed:
			return "warning", taskRunCodeInterruptedAfterProviderCommit, true
		}
	}
	if !outcome.ProviderCommitRecorded && outcome.State == backupasset.RecoveryPointFailed {
		return "failed", taskRunCodeInterruptedBeforeProviderCommit, true
	}
	return "", "", false
}

func (manager *Manager) reportInterruptedPublication(ctx context.Context, taskID, taskRunID uint, targetStatus, code string) error {
	if taskID == 0 || taskRunID == 0 || (targetStatus != "warning" && targetStatus != "failed") || code == "" {
		return fmt.Errorf("%w: invalid interrupted publication report", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	return manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.TaskRun
		if err := tx.Where("id = ? AND task_id = ? AND status IN ?", taskRunID, taskID, []string{"pending", "running", "retrying"}).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("load interrupted TaskRun: %w", err)
		}
		updated := tx.Model(&model.TaskRun{}).
			Where("id = ? AND task_id = ? AND status IN ?", taskRunID, taskID, []string{"pending", "running", "retrying"}).
			Updates(map[string]any{"status": targetStatus, "finished_at": &now, "last_error": code})
		if updated.Error != nil {
			return fmt.Errorf("mark interrupted TaskRun: %w", updated.Error)
		}
		if updated.RowsAffected != 1 || run.StartedAt == nil {
			return nil
		}
		// The aggregate update is intentionally a precise CAS: a subsequent run
		// must never have its Task status overwritten by this stale reporter.
		result := tx.Model(&model.Task{}).
			Where("id = ? AND status = ? AND last_run_at = ?", taskID, "running", run.StartedAt.UTC()).
			Where("NOT EXISTS (SELECT 1 FROM task_runs AS newer WHERE newer.task_id = ? AND newer.id <> ? AND newer.status IN ?)", taskID, taskRunID, []string{"pending", "running", "retrying"}).
			Updates(map[string]any{"status": targetStatus, "last_error": code})
		if result.Error != nil {
			return fmt.Errorf("mark interrupted Task aggregate: %w", result.Error)
		}
		return nil
	})
}

// ReconcileInterruptedRuns performs the restart-only stale-run scan. It owns
// TaskRun queries rather than leaking them into the asset runtime; the final
// unfiltered query is the readiness gate and catches rows beyond this pass.
func (manager *Manager) ReconcileInterruptedRuns(ctx context.Context, limit int) (bool, error) {
	if manager == nil || manager.db == nil || limit <= 0 {
		return false, fmt.Errorf("%w: interrupted publication readiness unavailable", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var runs []model.TaskRun
	if err := manager.db.WithContext(ctx).Table("task_runs").
		Select("task_runs.*").Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("LOWER(tasks.executor_type) = ? AND task_runs.status IN ?", "restic", []string{"pending", "running", "retrying"}).
		Order("task_runs.id ASC").Limit(limit).Find(&runs).Error; err != nil {
		return false, fmt.Errorf("list interrupted Restic TaskRuns: %w", err)
	}
	for _, run := range runs {
		if _, live := manager.pendingRuns.Load(run.TaskID); live {
			continue
		}
		var point model.RecoveryPoint
		result := manager.db.WithContext(ctx).Where("producing_task_run_id = ?", run.ID).Limit(1).Find(&point)
		if result.Error != nil {
			return false, fmt.Errorf("load interrupted publication point: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			continue
		}
		outcome, reportable, err := interruptedOutcomeFromPoint(point, run)
		if err != nil {
			return false, err
		}
		if !reportable {
			continue
		}
		if err := manager.reportInterruptedPublication(ctx, outcome.TaskID, outcome.TaskRunID, interruptedOutcomeStatus(outcome), interruptedOutcomeCode(outcome)); err != nil {
			return false, err
		}
	}
	var remaining int64
	if err := manager.db.WithContext(ctx).Table("task_runs").
		Joins("JOIN tasks ON tasks.id = task_runs.task_id").
		Where("LOWER(tasks.executor_type) = ? AND task_runs.status IN ?", "restic", []string{"pending", "running", "retrying"}).
		Count(&remaining).Error; err != nil {
		return false, fmt.Errorf("count unresolved Restic TaskRuns: %w", err)
	}
	return remaining > 0, nil
}

func interruptedOutcomeFromPoint(point model.RecoveryPoint, run model.TaskRun) (publication.Outcome, bool, error) {
	if point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != run.ID || point.ProducingTaskID == nil || *point.ProducingTaskID != run.TaskID {
		return publication.Outcome{}, false, fmt.Errorf("%w: interrupted publication TaskRun ownership drift", backupasset.ErrConflict)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		return publication.Outcome{}, false, err
	}
	if lineage.TaskID != run.TaskID || lineage.TaskRunID != run.ID || lineage.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		return publication.Outcome{}, false, fmt.Errorf("%w: interrupted publication immutable lineage drift", backupasset.ErrConflict)
	}
	consistency, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
	if err != nil {
		return publication.Outcome{}, false, err
	}
	state := backupasset.RecoveryPointState(point.State)
	providerCommitRecorded := consistency.Provider == backupasset.ProviderRestic && consistency.ProviderCommitDigest != "" &&
		consistency.CaptureStartedAt != nil && consistency.CaptureFinishedAt != nil && strings.TrimSpace(point.EncryptedProviderLocator) != ""
	outcome := publication.Outcome{RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, TaskID: run.TaskID, TaskRunID: run.ID, State: state, ProviderCommitRecorded: providerCommitRecorded, Code: consistency.Code}
	_, _, reportable := interruptedPublicationTaskRunState(outcome)
	return outcome, reportable, nil
}

func interruptedOutcomeStatus(outcome publication.Outcome) string {
	status, _, _ := interruptedPublicationTaskRunState(outcome)
	return status
}

func interruptedOutcomeCode(outcome publication.Outcome) string {
	_, code, _ := interruptedPublicationTaskRunState(outcome)
	return code
}

var _ publication.InterruptedRunReporter = (*Manager)(nil)
var _ publication.InterruptedRunReadiness = (*Manager)(nil)
