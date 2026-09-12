package task

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const legacyRcloneReconcileReasonMaxRunes = 1024

// LegacyRcloneReconcileRequest is an explicit operator acknowledgement that a
// legacy Rclone remote write has stopped. It reconciles write evidence only; it
// does not establish a verified generation or authorize a restore.
type LegacyRcloneReconcileRequest struct {
	TaskID        uint
	TaskRunID     uint
	RemoteStopped bool
	Reason        string
	Actor         credentialaudit.Event
}

// ReconcileLegacyRcloneWrite converts one persisted legacy Rclone writing or
// unknown generation into dirty after an authenticated administrator confirms
// that the remote write has stopped. The transition, owner fencing, and audit
// record share one transaction.
func ReconcileLegacyRcloneWrite(ctx context.Context, db *gorm.DB, request LegacyRcloneReconcileRequest) error {
	reason, err := validateLegacyRcloneReconcileRequest(request)
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("%w: reconciliation database unavailable", apperr.ErrValidation)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// LockTaskIDsForUpdate performs a no-op name update on SQLite. That
		// update is the repository's cross-connection writer serialization
		// boundary; PostgreSQL additionally gets the row lock below.
		if err := gormrepo.LockTaskIDsForUpdate(tx, []uint{request.TaskID}); err != nil {
			return err
		}

		var taskEntity model.Task
		taskResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "node_id", "status", "enabled", "last_error", "next_run_at", "archived_at").
			Where("id = ?", request.TaskID).
			Limit(1).
			Find(&taskEntity)
		if taskResult.Error != nil {
			return apperr.WrapDBError(taskResult.Error)
		}
		if taskResult.RowsAffected != 1 {
			return fmt.Errorf("%w: task not found", apperr.ErrNotFound)
		}
		if taskEntity.ArchivedAt != nil {
			return fmt.Errorf("%w: task not found", apperr.ErrNotFound)
		}
		if taskEntity.Enabled {
			return fmt.Errorf("%w: task must be paused before reconciliation", apperr.ErrConflict)
		}

		var selected model.TaskRun
		runResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND task_id = ?", request.TaskRunID, request.TaskID).
			Limit(1).
			Find(&selected)
		if runResult.Error != nil {
			return apperr.WrapDBError(runResult.Error)
		}
		if runResult.RowsAffected != 1 {
			return fmt.Errorf("%w: task run not found", apperr.ErrNotFound)
		}

		oldState := strings.TrimSpace(selected.BackupGenerationState)
		if oldState != model.TaskRunGenerationStateWriting && oldState != model.TaskRunGenerationStateUnknown {
			return fmt.Errorf("%w: selected task run is not an unresolved legacy Rclone generation", apperr.ErrConflict)
		}
		trigger := strings.ToLower(strings.TrimSpace(selected.TriggerType))
		if trigger == "restore" || trigger == "drill" {
			return fmt.Errorf("%w: restore and drill runs cannot be reconciled as writes", apperr.ErrConflict)
		}
		if !model.IsKnownTaskRunStatus(selected.Status) {
			return fmt.Errorf("%w: selected task run has an unsupported status", apperr.ErrConflict)
		}

		now := time.Now().UTC()
		if selected.ExecutionLeaseUntil != nil && selected.ExecutionLeaseUntil.After(now) {
			return fmt.Errorf("%w: selected task run still has a live execution lease", apperr.ErrConflict)
		}
		if strings.TrimSpace(selected.ExecutionOwnerID) != "" && selected.ExecutionLeaseUntil == nil {
			// A non-empty owner without an expiry is an unbounded capability,
			// not evidence that the remote has stopped.
			return fmt.Errorf("%w: selected task run has an unbounded execution owner", apperr.ErrConflict)
		}

		selectedActive := model.IsActiveTaskRunStatus(selected.Status)
		if selectedActive {
			// An active writing generation can only be safely taken over when
			// it has an explicit expired lease. A missing lease is ambiguous.
			if oldState != model.TaskRunGenerationStateWriting || selected.ExecutionLeaseUntil == nil {
				return fmt.Errorf("%w: active writing task run must have an expired execution lease", apperr.ErrConflict)
			}
		}

		// Only active siblings matter for admission and race safety. Historical
		// terminal rows are evidence and need not be loaded or locked.
		var activeSibling model.TaskRun
		activeResult := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id").
			Where("task_id = ? AND id <> ? AND status IN ?", request.TaskID, request.TaskRunID, model.TaskRunActiveStatuses()).
			Order("id ASC").
			Limit(1).
			Find(&activeSibling)
		if activeResult.Error != nil {
			return apperr.WrapDBError(activeResult.Error)
		}
		if activeResult.RowsAffected != 0 {
			return fmt.Errorf("%w: another task run is still active", apperr.ErrConflict)
		}

		// A shared immutable Rclone resource cannot be reconciled while another
		// TaskRun still proves a writing/unknown generation on that same remote.
		// Historical rows without a key remain conservatively node-scoped.
		unresolvedQuery := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id").
			Where("id <> ?", selected.ID).
			Where(`lower(COALESCE(trigger_type, '')) NOT IN ? AND
				TRIM(COALESCE(backup_generation_state, '')) IN ?`,
				[]string{"restore", "drill"},
				[]string{model.TaskRunGenerationStateWriting, model.TaskRunGenerationStateUnknown})
		if key := strings.TrimSpace(selected.ResourceKey); key != "" {
			unresolvedQuery = unresolvedQuery.Where("resource_key = ?", key)
		} else {
			unresolvedQuery = unresolvedQuery.Where("node_id_snapshot = ?", selected.NodeIDSnapshot).
				Where("TRIM(COALESCE(executor_type_snapshot, '')) = '' OR TRIM(COALESCE(resource_key, '')) = ''")
		}
		var unresolvedSiblings []model.TaskRun
		if result := unresolvedQuery.Find(&unresolvedSiblings); result.Error != nil {
			return apperr.WrapDBError(result.Error)
		} else if len(unresolvedSiblings) > 0 {
			return fmt.Errorf("%w: another task run still holds the shared mutable remote", apperr.ErrConflict)
		}

		// An active selected writing run was abandoned by its expired owner.
		// Settle its aggregate only when the aggregate is itself in an active
		// state; paused state and diagnostic text remain untouched.
		if selectedActive {
			taskStatus := ParseStatus(taskEntity.Status)
			if taskStatus == StatusRunning || taskStatus == StatusRetrying {
				updated := tx.WithContext(ctx).Model(&model.Task{}).
					Where("id = ? AND status = ?", taskEntity.ID, taskEntity.Status).
					Update("status", string(StatusFailed))
				if updated.Error != nil {
					return apperr.WrapDBError(updated.Error)
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("%w: task status changed during reconciliation", apperr.ErrConflict)
				}
			}
		}

		runUpdates := map[string]any{
			"backup_generation_state": model.TaskRunGenerationStateDirty,
			"execution_owner_id":      "",
			"execution_lease_until":   nil,
		}
		if selectedActive {
			finishedAt := now
			runUpdates["status"] = model.TaskRunStatusFailed
			runUpdates["finished_at"] = &finishedAt
			durationMs := int64(0)
			if selected.StartedAt != nil {
				durationMs = finishedAt.Sub(selected.StartedAt.UTC()).Milliseconds()
				if durationMs < 0 {
					durationMs = 0
				}
			}
			runUpdates["duration_ms"] = durationMs
		}
		runUpdate := tx.WithContext(ctx).Model(&model.TaskRun{}).
			Where("id = ? AND task_id = ? AND status = ? AND TRIM(COALESCE(backup_generation_state, '')) = ?",
				selected.ID, request.TaskID, selected.Status, oldState).
			Updates(runUpdates)
		if runUpdate.Error != nil {
			return apperr.WrapDBError(runUpdate.Error)
		}
		if runUpdate.RowsAffected != 1 {
			return fmt.Errorf("%w: selected task run changed during reconciliation", apperr.ErrConflict)
		}

		taskID := request.TaskID
		runID := request.TaskRunID
		auditEvent := credentialaudit.Event{
			UserID:    request.Actor.UserID,
			Username:  request.Actor.Username,
			Role:      request.Actor.Role,
			Action:    "task.legacy_rclone_reconcile",
			Purpose:   sshutil.PurposeTaskCommand,
			NodeID:    credentialaudit.PtrUint(selected.NodeIDSnapshot),
			TaskID:    &taskID,
			TaskRunID: &runID,
			Outcome:   credentialaudit.OutcomeSuccess,
			ClientIP:  request.Actor.ClientIP,
			UserAgent: request.Actor.UserAgent,
			Metadata: map[string]any{
				"old_state":      oldState,
				"new_state":      model.TaskRunGenerationStateDirty,
				"remote_stopped": true,
				"reason":         reason,
			},
		}
		if err := credentialaudit.Write(tx, auditEvent); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return apperr.WrapDBError(err)
	}
	return nil
}

func validateLegacyRcloneReconcileRequest(request LegacyRcloneReconcileRequest) (string, error) {
	if request.TaskID == 0 || request.TaskRunID == 0 {
		return "", fmt.Errorf("%w: task and task run IDs are required", apperr.ErrValidation)
	}
	if !request.RemoteStopped {
		return "", fmt.Errorf("%w: remote_stopped must be explicitly true", apperr.ErrValidation)
	}
	if request.Actor.UserID == 0 || strings.TrimSpace(request.Actor.Role) != "admin" {
		return "", fmt.Errorf("%w: authenticated administrator actor is required", apperr.ErrValidation)
	}
	rawReason := strings.TrimSpace(request.Reason)
	if rawReason == "" {
		return "", fmt.Errorf("%w: reconciliation reason is required", apperr.ErrValidation)
	}
	if utf8.RuneCountInString(rawReason) > legacyRcloneReconcileReasonMaxRunes {
		return "", fmt.Errorf("%w: reconciliation reason exceeds 1024 runes", apperr.ErrValidation)
	}
	reason := strings.TrimSpace(util.SanitizeMessage(rawReason))
	if reason == "" || utf8.RuneCountInString(reason) > legacyRcloneReconcileReasonMaxRunes {
		return "", fmt.Errorf("%w: reconciliation reason is invalid", apperr.ErrValidation)
	}
	return reason, nil
}
