package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/backuphealth"
	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
)

const (
	defaultTaskRunLease       = 30 * time.Second
	taskRunEffectLease        = 30 * time.Second
	taskRunEffectMaxAttempts  = 8
	taskRunEffectBatchSize    = 32
	taskRunRecoveryBatchSize  = 64
	taskRunRecoveryGrace      = 2 * time.Minute
	taskRunEffectBackoffLimit = 5 * time.Minute
)

var (
	errTaskRunNotOwner = errors.New("task run is owned by another live process")
	errTaskRunCASLost  = errors.New("task run terminal transition compare-and-swap lost")
)

func durableTaskContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if ctx.Err() != nil {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

// failTaskRunBeforeExecutor durably rejects a malformed or foreign TaskRun
// without changing the aggregate Task. This path is intentionally keyed only
// by the durable run identity: a runner must be able to close a row it was
// handed even when the row's TaskID or immutable node snapshot is invalid,
// while a live owner from another process remains fenced.
func (m *Manager) failTaskRunBeforeExecutor(ctx context.Context, runID uint, message string) error {
	if m == nil || m.db == nil || runID == 0 {
		return errors.New("task run rejection persistence unavailable")
	}
	ctx = durableTaskContext(ctx)
	now := time.Now().UTC()
	sanitizedMessage := sanitizeTaskLastError(message)
	var effects []taskRunTerminalEffect
	var provenanceErr error
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Read the aggregate identity without a lock, then acquire the Task
		// lock before the TaskRun lock. Cancellation and ordinary terminalization
		// use this same Task -> TaskRun order.
		var identity struct {
			TaskID uint `gorm:"column:task_id"`
		}
		identityResult := tx.Model(&model.TaskRun{}).
			Select("task_id").
			Where("id = ?", runID).
			Limit(1).
			Find(&identity)
		if identityResult.Error != nil {
			return identityResult.Error
		}
		if identityResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Preload("Policy").
			Where("id = ?", identity.TaskID).
			Limit(1).
			Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		taskMissing := taskResult.RowsAffected != 1

		var run model.TaskRun
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", runID).
			Limit(1).
			Find(&run)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		// The unlocked identity read only chose which aggregate to lock. Do
		// not mutate a row that was rebound to a different aggregate while the
		// Task lock was being acquired.
		if run.TaskID != identity.TaskID {
			return errTaskRunCASLost
		}
		if model.IsTerminalTaskRunStatus(run.Status) {
			return nil
		}
		if !model.IsActiveTaskRunStatus(run.Status) {
			return errTaskRunCASLost
		}
		if strings.TrimSpace(run.ExecutionOwnerID) != "" && run.ExecutionOwnerID != m.executionOwnerID {
			return errTaskRunNotOwner
		}
		markNoStart, err := rcloneNoStartStateForRunTx(tx, &run)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"status":                model.TaskRunStatusFailed,
			"finished_at":           &now,
			"last_error":            sanitizedMessage,
			"execution_owner_id":    "",
			"execution_lease_until": nil,
		}
		if markNoStart && strings.TrimSpace(run.BackupGenerationState) == "" {
			updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
		}
		if run.Status == model.TaskRunStatusPending {
			updates["started_at"] = nil
			updates["duration_ms"] = int64(0)
		} else if run.StartedAt != nil {
			duration := now.Sub(run.StartedAt.UTC()).Milliseconds()
			if duration > 0 {
				updates["duration_ms"] = duration
			}
		}
		run.LastError = sanitizedMessage
		result = tx.Model(&model.TaskRun{}).
			Where("id = ? AND status = ?", run.ID, run.Status).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		// A malformed run can outlive its aggregate. It is still safe to close
		// the run after its identity was revalidated, but there is no aggregate
		// or effect work to perform.
		if taskMissing {
			return nil
		}

		currentRetry := false
		if strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") &&
			ParseStatus(taskEntity.Status) == StatusRetrying {
			currentRetry, err = isCurrentRetryRunTx(tx, run)
			if err != nil {
				return err
			}
		}

		effectTask := taskEntity
		retrySchedule := terminalRetrySchedule{}
		if currentRetry {
			nextStatus, newRetryCount, deadline, shouldRetry :=
				retryDeadlineAfterPreExecutorFailure(taskEntity, now, m.stateMachine)
			cursorMode, modeErr := retryCursorModeForTerminalRunTx(tx, taskEntity, run)
			if modeErr != nil {
				// The failure is still durable, but an unknown cursor
				// provenance cannot be interpreted as either legacy or
				// regular. End this retry cycle and clear the cursor so a
				// later reconciliation can explicitly rebuild it.
				provenanceErr = fmt.Errorf("decode retry cursor provenance: %w", modeErr)
				nextStatus = StatusFailed
				shouldRetry = false
				deadline = nil
			} else {
				retrySchedule.separate = cursorMode == model.TaskRunCronCursorModeRegularV1
			}
			if !shouldRetry {
				nextStatus = StatusFailed
			}
			taskUpdates := map[string]interface{}{
				"status":      string(nextStatus),
				"retry_count": newRetryCount,
				"last_error":  sanitizedMessage,
			}
			if shouldRetry {
				taskUpdates["next_run_at"] = deadline
				retrySchedule.deadline = deadline
			} else if provenanceErr != nil {
				taskUpdates["next_run_at"] = nil
			} else {
				taskUpdates["next_run_at"] = cronutil.Next(taskEntity.CronSpec)
			}
			taskStatus := nextStatus
			normalizeCronTerminalSchedule(taskEntity, run, &taskStatus, taskUpdates, retrySchedule)
			if err := m.stateMachine.ValidateTransition(ParseStatus(taskEntity.Status), taskStatus); err != nil {
				return err
			}
			taskResult = tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", taskEntity.ID, taskEntity.Status).
				Updates(taskUpdates)
			if taskResult.Error != nil {
				return taskResult.Error
			}
			if taskResult.RowsAffected != 1 {
				return errTaskRunCASLost
			}
			effectTask.Status = string(taskStatus)
			effectTask.RetryCount = newRetryCount
			effectTask.LastError = sanitizedMessage
			if nextValue, ok := taskUpdates["next_run_at"]; ok {
				switch next := nextValue.(type) {
				case *time.Time:
					if next == nil {
						effectTask.NextRunAt = nil
					} else {
						copied := next.UTC()
						effectTask.NextRunAt = &copied
					}
				case time.Time:
					copied := next.UTC()
					effectTask.NextRunAt = &copied
				case nil:
					effectTask.NextRunAt = nil
				}
			}
		} else if ParseStatus(effectTask.Status) == StatusRetrying {
			// A malformed or stale non-retry run must not publish a retry
			// intent for the aggregate's unrelated retry cycle.
			effectTask.Status = string(StatusFailed)
		}

		var buildErr error
		effects, buildErr = buildTerminalEffects(tx, effectTask, run, run.ID, StatusFailed, retrySchedule)
		if buildErr != nil {
			return buildErr
		}
		return persistTaskRunEffectsTx(tx, run.ID, effects)
	})
	if err != nil {
		return err
	}
	if drainErr := m.drainTaskRunEffects(ctx, runID); drainErr != nil {
		if provenanceErr != nil {
			return errors.Join(provenanceErr, drainErr)
		}
		return drainErr
	}
	return provenanceErr
}

// cancelRestoreTaskRunBeforeExecutor records cancellation for a restore run
// without touching the aggregate Task. The guarded update is issued even when
// another path already committed a terminal cancellation; this gives the
// runner an observable compare-and-swap boundary while the status predicate
// prevents overwriting a newer terminal result.
func (m *Manager) cancelRestoreTaskRunBeforeExecutor(ctx context.Context, taskID, runID uint, message string) error {
	if m == nil || m.db == nil || taskID == 0 || runID == 0 {
		return errors.New("restore cancellation persistence unavailable")
	}
	ctx = durableTaskContext(ctx)
	now := time.Now().UTC()
	var current model.TaskRun
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loaded := tx.Where("id = ? AND task_id = ?", runID, taskID).Limit(1).Find(&current)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		updates := map[string]interface{}{
			"status":                model.TaskRunStatusCanceled,
			"finished_at":           &now,
			"last_error":            sanitizeTaskLastError(message),
			"execution_owner_id":    "",
			"execution_lease_until": nil,
		}
		if current.Status == model.TaskRunStatusPending {
			updates["started_at"] = nil
			updates["duration_ms"] = int64(0)
		} else if current.StartedAt != nil {
			duration := now.Sub(current.StartedAt.UTC()).Milliseconds()
			if duration < 0 {
				duration = 0
			}
			updates["duration_ms"] = duration
		}
		result := tx.Model(&model.TaskRun{}).
			Where(`id = ? AND task_id = ? AND status IN ? AND
				(execution_owner_id = '' OR execution_owner_id = ? OR
					execution_lease_until IS NULL OR execution_lease_until <= ?)`,
				runID, taskID, model.TaskRunActiveStatuses(), m.executionOwnerID, now).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 || model.IsTerminalTaskRunStatus(current.Status) {
			return nil
		}
		if strings.TrimSpace(current.ExecutionOwnerID) != "" &&
			current.ExecutionOwnerID != m.executionOwnerID &&
			(current.ExecutionLeaseUntil == nil || current.ExecutionLeaseUntil.After(now)) {
			return errTaskRunNotOwner
		}
		return errTaskRunCASLost
	})
}

// cancelOwnedRunningTask closes a running TaskRun and its active aggregate in
// one transaction after the caller has signaled the process-local owner. The
// Task lock is the serialization boundary for the retrying aggregate; no
// failure effects are published for a user cancellation.
func (m *Manager) cancelOwnedRunningTask(taskID uint, message string) (bool, error) {
	if m == nil || m.db == nil || taskID == 0 {
		return false, errors.New("running task cancellation persistence unavailable")
	}
	if strings.TrimSpace(m.executionOwnerID) == "" {
		return false, nil
	}
	now := time.Now().UTC()
	sanitizedMessage := sanitizeTaskLastError(message)
	canceled := false
	previousTaskOutcome, hasPreviousOutcome := m.previousTaskOutcomeForTask(taskID)
	if !m.claimProviderCancellation(taskID) {
		return false, nil
	}

	legacyReanchor := false
	var provenanceErr error
	err := m.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		var runningRuns []model.TaskRun
		runResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND status = ?", taskID, model.TaskRunStatusRunning).
			Order("id ASC").Find(&runningRuns)
		if runResult.Error != nil {
			return runResult.Error
		}
		ownedRuns := make([]model.TaskRun, 0, len(runningRuns))
		for _, run := range runningRuns {
			owner := strings.TrimSpace(run.ExecutionOwnerID)
			if owner != "" && owner != m.executionOwnerID {
				return errTaskRunNotOwner
			}
			if owner == m.executionOwnerID {
				ownedRuns = append(ownedRuns, run)
			}
		}
		if len(ownedRuns) == 0 {
			if ParseStatus(taskEntity.Status) == StatusRunning {
				return errTaskCancelConflict
			}
			return nil
		}
		canceled = true

		var retryRun model.TaskRun
		for index := len(ownedRuns) - 1; index >= 0; index-- {
			if strings.EqualFold(strings.TrimSpace(ownedRuns[index].TriggerType), "retry") {
				retryRun = ownedRuns[index]
				break
			}
		}
		retryCursorMode := model.TaskRunCronCursorModeLegacy
		if retryRun.ID != 0 && strings.TrimSpace(taskEntity.CronSpec) != "" {
			var modeErr error
			retryCursorMode, modeErr = retryCursorModeForTerminalRunTx(tx, taskEntity, retryRun)
			if modeErr != nil {
				// The cancellation is still committed, but no wall-clock
				// value may be invented for an unknown cursor owner.
				provenanceErr = fmt.Errorf("decode retry cursor provenance: %w", modeErr)
			}
		}

		for _, run := range ownedRuns {
			markNoStart, markErr := rcloneNoStartStateForPreProviderRunTx(tx, &run)
			if markErr != nil {
				return markErr
			}
			updates := map[string]interface{}{
				"status":                model.TaskRunStatusCanceled,
				"finished_at":           &now,
				"last_error":            sanitizedMessage,
				"execution_owner_id":    "",
				"execution_lease_until": nil,
			}
			updates["started_at"] = nil
			updates["duration_ms"] = int64(0)
			if markNoStart {
				updates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
			}
			updated := tx.Model(&model.TaskRun{}).
				Where("id = ? AND task_id = ? AND status = ? AND execution_owner_id = ?",
					run.ID, taskID, model.TaskRunStatusRunning, m.executionOwnerID).
				Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errTaskCancelConflict
			}
		}

		taskStatus := ParseStatus(taskEntity.Status)
		if hasPreviousOutcome && taskStatus == StatusRunning && retryRun.ID == 0 {
			taskUpdates := map[string]interface{}{
				"status":      previousTaskOutcome.Status,
				"last_run_at": previousTaskOutcome.LastRunAt,
				"last_error":  previousTaskOutcome.LastError,
			}
			if strings.TrimSpace(previousTaskOutcome.CronSpec) == "" {
				taskUpdates["next_run_at"] = previousTaskOutcome.NextRunAt
			} else if ParseStatus(previousTaskOutcome.Status) == StatusRetrying {
				cursorMode, modeErr := retryCursorModeForTerminalRunTx(tx, previousTaskOutcome, retryRun)
				if modeErr != nil {
					return modeErr
				}
				if cursorMode == model.TaskRunCronCursorModeRegularV1 {
					taskUpdates["next_run_at"] = previousTaskOutcome.NextRunAt
				} else {
					next := cronutil.Next(previousTaskOutcome.CronSpec)
					taskUpdates["next_run_at"] = next
					legacyReanchor = next != nil
				}
			}
			updated := tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", taskID, string(StatusRunning)).
				Updates(taskUpdates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errTaskCancelConflict
			}
			return nil
		}
		if taskStatus != StatusPending && taskStatus != StatusRunning && taskStatus != StatusRetrying {
			return nil
		}
		taskUpdates := map[string]interface{}{
			"status":     string(StatusCanceled),
			"last_error": sanitizedMessage,
		}
		if strings.TrimSpace(taskEntity.CronSpec) == "" ||
			!taskEntity.Enabled || taskEntity.ArchivedAt != nil {
			taskUpdates["next_run_at"] = nil
		} else if retryRun.ID != 0 &&
			(taskStatus == StatusRetrying || strings.EqualFold(strings.TrimSpace(retryRun.TriggerType), "retry")) {
			if provenanceErr != nil {
				taskUpdates["next_run_at"] = nil
			} else if retryCursorMode != model.TaskRunCronCursorModeRegularV1 {
				next := cronutil.Next(taskEntity.CronSpec)
				taskUpdates["next_run_at"] = next
				legacyReanchor = next != nil
			}
		}
		updated := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", taskID, taskEntity.Status).
			Updates(taskUpdates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errTaskCancelConflict
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if legacyReanchor {
		if err := m.SyncSchedule(model.Task{ID: taskID}); err != nil {
			if provenanceErr != nil {
				return true, errors.Join(provenanceErr, err)
			}
			return true, fmt.Errorf("reconcile legacy retry schedule after cancellation: %w", err)
		}
	}
	if provenanceErr != nil {
		return true, provenanceErr
	}
	return canceled, nil
}

type taskRunTerminalEffect struct {
	Key           string
	Type          string
	Payload       string
	NextAttemptAt *time.Time
}

type automationTaskRunEffect struct {
	EventType string                 `json:"event_type"`
	Context   map[string]interface{} `json:"context"`
}

type retryTaskRunEffect struct {
	TaskID            uint   `json:"task_id"`
	ChainRunID        string `json:"chain_run_id"`
	UpstreamTaskRunID *uint  `json:"upstream_task_run_id,omitempty"`
	PredecessorRunID  uint   `json:"predecessor_run_id"`
	// Current cron retry effects carry their retry deadline in
	// TaskRunEffect.NextAttemptAt while Task.NextRunAt remains the regular
	// cron cursor. An omitted mode is the pre-cutover legacy format, where
	// Task.NextRunAt was also the retry deadline.
	CronCursorMode string `json:"cron_cursor_mode,omitempty"`
}

func retryPredecessorRunIDTx(tx *gorm.DB, run model.TaskRun) (uint, error) {
	if tx == nil || run.ID == 0 || run.TaskID == 0 ||
		!strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") {
		return 0, nil
	}
	var candidates []model.TaskRun
	result := tx.Where(`task_id = ? AND id < ? AND
		lower(COALESCE(trigger_type, '')) NOT IN ? AND status = ? AND
		EXISTS (
			SELECT 1 FROM task_run_effects AS retry_effect
			WHERE retry_effect.task_run_id = task_runs.id
				AND retry_effect.effect_type = ?
		)`,
		run.TaskID, run.ID, []string{"drill", "restore"},
		model.TaskRunStatusFailed, model.TaskRunEffectTypeRetry).
		Order("id DESC").Limit(taskRunRecoveryBatchSize).Find(&candidates)
	if result.Error != nil {
		return 0, result.Error
	}
	for _, candidate := range candidates {
		var effect model.TaskRunEffect
		effectResult := tx.Where("task_run_id = ? AND effect_type = ?", candidate.ID, model.TaskRunEffectTypeRetry).
			Order("id DESC").Limit(1).Find(&effect)
		if effectResult.Error != nil {
			return 0, effectResult.Error
		}
		if effectResult.RowsAffected != 1 {
			continue
		}
		if _, err := model.ParseTaskRunEffectCronCursorMode(effect.Payload); err != nil {
			return 0, err
		}
		var payload retryTaskRunEffect
		if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
			return 0, fmt.Errorf("decode predecessor retry effect: %w", err)
		}
		if payload.PredecessorRunID != candidate.ID {
			continue
		}
		if run.ChainRunID != "" {
			if payload.ChainRunID != "" && payload.ChainRunID != run.ChainRunID {
				continue
			}
			if payload.ChainRunID == "" && candidate.ChainRunID != run.ChainRunID {
				continue
			}
		}
		return candidate.ID, nil
	}
	return 0, nil
}

// retryCursorModeForTerminalRunTx derives a retry attempt's cron ownership
// from the exact predecessor effect. A newly published cron failure has no
// predecessor effect and therefore starts in the current regular-cursor mode.
func retryCursorModeForTerminalRunTx(
	tx *gorm.DB,
	taskEntity model.Task,
	run model.TaskRun,
) (model.TaskRunCronCursorMode, error) {
	if strings.TrimSpace(taskEntity.CronSpec) == "" {
		return model.TaskRunCronCursorModeLegacy, nil
	}
	if !strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") {
		return model.TaskRunCronCursorModeRegularV1, nil
	}
	predecessorID, err := retryPredecessorRunIDTx(tx, run)
	if err != nil {
		return model.TaskRunCronCursorModeLegacy, err
	}
	return model.RetryEffectCronCursorModeTx(tx, predecessorID)
}

func isCurrentRetryRunTx(tx *gorm.DB, run model.TaskRun) (bool, error) {
	if tx == nil || run.ID == 0 || run.TaskID == 0 ||
		!strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") {
		return false, nil
	}
	var latest model.TaskRun
	result := tx.Where(`task_id = ? AND
		lower(COALESCE(trigger_type, '')) NOT IN ?`,
		run.TaskID, []string{"drill", "restore"}).
		Order("id DESC").Limit(1).Find(&latest)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1 && latest.ID == run.ID, nil
}

// retryDeadlineAfterPreExecutorFailure returns the complete state-machine
// decision. A nil deadline is meaningful only when shouldRetry is false; the
// caller must never infer a retry from a missing deadline.
func retryDeadlineAfterPreExecutorFailure(
	taskEntity model.Task,
	now time.Time,
	sm *StateMachine,
) (TaskStatus, int, *time.Time, bool) {
	if sm == nil {
		sm = NewStateMachine()
	}
	nextStatus, newRetryCount, nextRun, shouldRetry := StatusFailed, taskEntity.RetryCount, time.Time{}, false
	if taskEntity.Policy != nil && taskEntity.Policy.MaxRetries >= 0 {
		nextStatus, newRetryCount, nextRun, shouldRetry = sm.NextAfterFailureConfigurable(
			StatusRetrying, taskEntity.RetryCount, now,
			taskEntity.Policy.MaxRetries, taskEntity.Policy.RetryBaseSeconds,
		)
	} else {
		nextStatus, newRetryCount, nextRun, shouldRetry = sm.NextAfterFailure(
			StatusRetrying, taskEntity.RetryCount, now,
		)
	}
	if !shouldRetry || nextStatus != StatusRetrying || nextRun.IsZero() {
		return nextStatus, newRetryCount, nil, false
	}
	nextRun = nextRun.UTC()
	return nextStatus, newRetryCount, &nextRun, true
}

type terminalRetrySchedule struct {
	separate bool
	deadline *time.Time
}

func retryDeadlineFromTaskUpdates(taskUpdates map[string]interface{}) *time.Time {
	if taskUpdates == nil {
		return nil
	}
	switch value := taskUpdates["next_run_at"].(type) {
	case *time.Time:
		if value == nil || value.IsZero() {
			return nil
		}
		next := value.UTC()
		return &next
	case time.Time:
		if value.IsZero() {
			return nil
		}
		next := value.UTC()
		return &next
	default:
		return nil
	}
}

type downstreamTaskRunEffect struct {
	TaskID        uint   `json:"task_id"`
	UpstreamRunID uint   `json:"upstream_run_id"`
	ChainRunID    string `json:"chain_run_id"`
}

type alertTaskRunEffect struct {
	Action  string `json:"action"`
	TaskID  uint   `json:"task_id"`
	RunID   uint   `json:"run_id"`
	Message string `json:"message"`
}

// terminalizeTaskRun is the only ordinary Task/TaskRun terminal write path.
// It keeps the database transaction short (execution and transfer happen
// outside it), locks both rows, checks the expected states, and requires one
// affected row for every CAS. A retrying aggregate with a failed run is a
// legal pair, so taskStatus is deliberately independent from runStatus.
// Pending runs with another active owner are fenced from aggregate/effect
// mutation inside this same transaction; recovery mode derives the aggregate
// transition from the locked Task row rather than a preflight read.
func (m *Manager) terminalizeTaskRun(
	ctx context.Context,
	taskID, runID uint,
	expectedRunStatuses []string,
	taskStatus *TaskStatus,
	taskUpdates map[string]interface{},
	runStatus TaskStatus,
	runUpdates map[string]interface{},
	legacyFact ...bool,
) error {
	if err := m.terminalizeTaskRunTx(ctx, taskID, runID, expectedRunStatuses,
		taskStatus, taskUpdates, runStatus, runUpdates, terminalizeTaskRunModeNormal, legacyFact...); err != nil {
		return err
	}
	m.reconcileRetryCronScheduleAfterTerminal(taskID, runID)
	return nil
}

// reconcileRetryCronScheduleAfterTerminal closes the legacy compatibility
// boundary. Pre-cutover retry rows used Task.NextRunAt for the retry deadline,
// so SyncSchedule fenced their live entry while retrying. Once that retry
// terminalizes, the durable terminal writer has chosen the new regular
// cursor; register the live schedule from that same committed value.
func (m *Manager) reconcileRetryCronScheduleAfterTerminal(taskID, runID uint) {
	if m == nil || m.db == nil || m.scheduler == nil || taskID == 0 || runID == 0 {
		return
	}
	var run struct {
		TriggerType string `gorm:"column:trigger_type"`
	}
	result := m.db.Model(&model.TaskRun{}).
		Select("trigger_type").
		Where("id = ? AND task_id = ?", runID, taskID).
		Limit(1).Find(&run)
	if result.Error != nil || result.RowsAffected != 1 ||
		!strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") {
		if result.Error != nil {
			logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).
				Err(result.Error).Msg("load retry schedule terminal provenance")
		}
		return
	}
	if err := m.SyncSchedule(model.Task{ID: taskID}); err != nil {
		logger.Module("task").Warn().Uint("task_id", taskID).Uint("task_run_id", runID).
			Err(err).Msg("reconcile retry cron schedule after terminal")
	}
}

type terminalizeTaskRunMode uint8

const (
	terminalizeTaskRunModeNormal terminalizeTaskRunMode = iota
	terminalizeTaskRunModeRecovery
	terminalizeTaskRunModePreProviderFailure
)

// normalizeCronTerminalSchedule prevents terminal writers from recomputing a
// normal cron cursor from completion time. For current retry effects the
// explicit backoff deadline lives on TaskRunEffect.NextAttemptAt, so the
// durable Task.NextRunAt cursor remains untouched. Legacy retry effects omit
// the cursor mode and retain their historical Task.NextRunAt behavior.
func normalizeCronTerminalSchedule(
	taskEntity model.Task,
	run model.TaskRun,
	taskStatus *TaskStatus,
	taskUpdates map[string]interface{},
	retrySchedule ...terminalRetrySchedule,
) {
	if taskUpdates == nil || strings.TrimSpace(taskEntity.CronSpec) == "" {
		return
	}
	separateRetryCursor := len(retrySchedule) > 0 && retrySchedule[0].separate
	if taskStatus != nil && *taskStatus == StatusRetrying {
		if strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") && !separateRetryCursor {
			// A legacy retrying row still uses Task.NextRunAt as its retry
			// reservation until its effect is consumed.
			return
		}
		// Current cron failures keep the regular cursor on Task while the
		// retry effect stores the attempt deadline separately.
		delete(taskUpdates, "next_run_at")
		return
	}
	if !taskEntity.Enabled || taskEntity.ArchivedAt != nil {
		taskUpdates["next_run_at"] = nil
		return
	}
	if strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") {
		if separateRetryCursor {
			// The retry attempt must not re-anchor @every from completion time.
			delete(taskUpdates, "next_run_at")
		}
		return
	}
	if ParseStatus(taskEntity.Status) == StatusRetrying {
		// A non-retry run cannot consume a retry reservation owned by another
		// attempt. Leave its deadline untouched.
		delete(taskUpdates, "next_run_at")
		return
	}
	// Normal cron/manual terminalization preserves the current durable cursor.
	delete(taskUpdates, "next_run_at")
}

func (m *Manager) terminalizeTaskRunTx(
	ctx context.Context,
	taskID, runID uint,
	expectedRunStatuses []string,
	taskStatus *TaskStatus,
	taskUpdates map[string]interface{},
	runStatus TaskStatus,
	runUpdates map[string]interface{},
	mode terminalizeTaskRunMode,
	legacyFact ...bool,
) error {
	recordLegacyFact := len(legacyFact) > 0 && legacyFact[0]
	if m == nil || m.db == nil {
		return errors.New("task terminal persistence unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(expectedRunStatuses) == 0 {
		expectedRunStatuses = model.TaskRunActiveStatuses()
	}
	if !model.IsTerminalTaskRunStatus(string(runStatus)) {
		return fmt.Errorf("invalid ordinary terminal TaskRun status %q", runStatus)
	}
	if taskStatus != nil && !model.IsKnownTaskRunStatus(string(*taskStatus)) {
		return fmt.Errorf("invalid ordinary terminal Task status %q", *taskStatus)
	}
	if runUpdates == nil {
		runUpdates = map[string]interface{}{}
	} else {
		copied := make(map[string]interface{}, len(runUpdates)+4)
		for key, value := range runUpdates {
			copied[key] = value
		}
		runUpdates = copied
	}
	ctx = durableTaskContext(ctx)
	if taskUpdates == nil {
		taskUpdates = map[string]interface{}{}
	} else {
		copied := make(map[string]interface{}, len(taskUpdates)+2)
		for key, value := range taskUpdates {
			copied[key] = value
		}
		taskUpdates = copied
	}

	finishedAt := time.Now().UTC()
	if _, ok := runUpdates["finished_at"]; !ok {
		runUpdates["finished_at"] = &finishedAt
	}
	// A terminal row must never retain an execution lease. Clearing ownership
	// also lets a replacement process distinguish settled history from a stale
	// active owner during recovery.
	runUpdates["status"] = string(runStatus)
	runUpdates["execution_owner_id"] = ""
	runUpdates["execution_lease_until"] = nil
	suppressEffects := false

	var effects []taskRunTerminalEffect
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		var run model.TaskRun
		runResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND status IN ?", runID, taskID, expectedRunStatuses).
			Limit(1).Find(&run)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			// A concurrent terminal winner is authoritative. Treat an already
			// settled run with the requested status as idempotent, but never
			// overwrite a different terminal result.
			var settled model.TaskRun
			settledResult := tx.Where("id = ? AND task_id = ?", runID, taskID).Limit(1).Find(&settled)
			if settledResult.Error != nil {
				return settledResult.Error
			}
			if settledResult.RowsAffected == 1 && settled.Status == string(runStatus) {
				return nil
			}
			return errTaskRunCASLost
		}
		if model.IsTaskRunNodeSnapshotAuthoritative(run.NodeIDSnapshot) &&
			taskEntity.NodeID != run.NodeIDSnapshot {
			return errTaskRunCASLost
		}
		// A non-empty execution owner is a capability, not merely a lease
		// hint. Even after its lease expires, a stale runner cannot terminalize
		// after another process has (or may have) claimed the row. Historical
		// rows with an empty owner remain eligible for compatibility recovery.
		if strings.TrimSpace(run.ExecutionOwnerID) != "" && run.ExecutionOwnerID != m.executionOwnerID {
			return errTaskRunNotOwner
		}
		markNoStart, markErr := rcloneNoStartStateForRunTx(tx, &run)
		if markErr != nil {
			return markErr
		}
		if markNoStart && strings.TrimSpace(run.BackupGenerationState) == "" {
			runUpdates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
		}
		if mode == terminalizeTaskRunModePreProviderFailure {
			if run.Status != model.TaskRunStatusRunning ||
				strings.TrimSpace(run.ExecutionOwnerID) != m.executionOwnerID {
				return errTaskRunNotOwner
			}
			preProviderNoStart, proofErr := rcloneNoStartStateForPreProviderRunTx(tx, &run)
			if proofErr != nil {
				return proofErr
			}
			if preProviderNoStart {
				runUpdates["backup_generation_state"] = model.TaskRunGenerationStateNoStart
			}
		}
		if mode == terminalizeTaskRunModeRecovery && taskStatus == nil {
			currentStatus := ParseStatus(taskEntity.Status)
			if currentStatus == StatusRunning || currentStatus == StatusRetrying {
				failed := StatusFailed
				taskStatus = &failed
				if _, ok := taskUpdates["next_run_at"]; !ok {
					taskUpdates["next_run_at"] = cronutil.Next(taskEntity.CronSpec)
				}
			}
		}
		if run.Status == model.TaskRunStatusPending {
			var activeRun model.TaskRun
			activeResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(`task_id = ? AND id <> ? AND status IN ? AND
					TRIM(COALESCE(execution_owner_id, '')) <> ''`,
					taskID, runID, model.TaskRunActiveStatuses()).
				Limit(1).Find(&activeRun)
			if activeResult.Error != nil {
				return activeResult.Error
			}
			if activeResult.RowsAffected == 1 {
				// A duplicate/lost runner must not fail the aggregate owned by
				// another active run. Its own TaskRun is still closed below,
				// but no retry/chain/alert effects are published for it.
				taskStatus = nil
				suppressEffects = true
			}
		}
		retrySchedule := terminalRetrySchedule{}
		if taskStatus != nil && *taskStatus == StatusRetrying {
			retrySchedule.deadline = retryDeadlineFromTaskUpdates(taskUpdates)
		}
		if strings.TrimSpace(taskEntity.CronSpec) != "" &&
			(taskStatus != nil && *taskStatus == StatusRetrying ||
				strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry")) {
			cursorMode, modeErr := retryCursorModeForTerminalRunTx(tx, taskEntity, run)
			if modeErr != nil {
				return modeErr
			}
			retrySchedule.separate = cursorMode == model.TaskRunCronCursorModeRegularV1
		}
		normalizeCronTerminalSchedule(taskEntity, run, taskStatus, taskUpdates, retrySchedule)

		if taskStatus != nil {
			from := ParseStatus(taskEntity.Status)
			if err := m.stateMachine.ValidateTransition(from, *taskStatus); err != nil {
				return err
			}
			taskUpdates["status"] = string(*taskStatus)
			result := tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", taskID, taskEntity.Status).
				Updates(taskUpdates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errTaskRunCASLost
			}
		}

		result := tx.Model(&model.TaskRun{}).
			Where("id = ? AND task_id = ? AND status = ?", runID, taskID, run.Status).
			Updates(runUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		if recordLegacyFact && runStatus == StatusSuccess &&
			strings.ToLower(strings.TrimSpace(run.TriggerType)) != "restore" &&
			strings.ToLower(strings.TrimSpace(run.TriggerType)) != "drill" &&
			isLegacyFactExecutor(run.ExecutorTypeSnapshot) {
			completedAt := terminalCompletionTime(runUpdates, finishedAt).Truncate(time.Microsecond)
			if err := backuphealth.RecordLegacyTransferTx(ctx, tx, backuphealth.LegacyTransferInput{
				TaskID:       run.TaskID,
				TaskRunID:    run.ID,
				NodeID:       run.NodeIDSnapshot,
				ExecutorType: run.ExecutorTypeSnapshot,
				CompletedAt:  completedAt,
			}); err != nil {
				return err
			}
		}
		// Effect intent must describe the committed aggregate and current attempt.
		if taskStatus != nil {
			taskEntity.Status = string(*taskStatus)
		}
		if message, ok := taskUpdates["last_error"].(string); ok {
			taskEntity.LastError = message
		}
		if message, ok := runUpdates["last_error"].(string); ok {
			run.LastError = message
		}
		if nextValue, ok := taskUpdates["next_run_at"]; ok {
			switch next := nextValue.(type) {
			case *time.Time:
				if next == nil {
					taskEntity.NextRunAt = nil
				} else {
					copied := next.UTC()
					taskEntity.NextRunAt = &copied
				}
			case time.Time:
				copied := next.UTC()
				taskEntity.NextRunAt = &copied
			case nil:
				taskEntity.NextRunAt = nil
			}
		}
		if suppressEffects {
			effects = nil
		} else {
			builtEffects, buildErr := buildTerminalEffects(tx, taskEntity, run, runID, runStatus, retrySchedule)
			if buildErr != nil {
				return buildErr
			}
			effects = builtEffects
		}
		if err := persistTaskRunEffectsTx(tx, runID, effects); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// The commit is the publication boundary. Every consumer below is
	// idempotent or keyed by the durable effect row; failures remain queued for
	// the bounded recovery worker rather than being silently discarded.
	if err := m.drainTaskRunEffects(ctx, runID); err != nil {
		logger.Module("task").Warn().Uint("task_run_id", runID).Err(err).Msg("drain task terminal effects")
	}
	return nil
}

// failTaskExecutionBeforeExecutor closes a running ordinary attempt only when
// the caller has synchronous, same-owner proof that no provider was invoked.
// The proof and terminal transition share the same row locks/transaction.
func (m *Manager) failTaskExecutionBeforeExecutor(
	ctx context.Context,
	taskID, runID uint,
	taskUpdates map[string]interface{},

	runUpdates map[string]interface{},
) error {
	failedStatus := StatusFailed
	if err := m.terminalizeTaskRunTx(
		ctx,
		taskID,
		runID,
		[]string{model.TaskRunStatusRunning},
		&failedStatus,
		taskUpdates,
		StatusFailed,
		runUpdates,
		terminalizeTaskRunModePreProviderFailure,
	); err != nil {
		return err
	}
	m.reconcileRetryCronScheduleAfterTerminal(taskID, runID)
	return nil
}
func isLegacyFactExecutor(executorType string) bool {
	switch strings.ToLower(strings.TrimSpace(executorType)) {
	case "rsync", "restic", "rclone":
		return true
	default:
		return false
	}
}

func terminalCompletionTime(runUpdates map[string]interface{}, fallback time.Time) time.Time {
	switch value := runUpdates["finished_at"].(type) {
	case *time.Time:
		if value != nil && !value.IsZero() {
			return value.UTC()
		}
	case time.Time:
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return fallback.UTC()
}

func buildTerminalEffects(
	tx *gorm.DB,
	taskEntity model.Task,
	run model.TaskRun,
	runID uint,
	runStatus TaskStatus,
	retrySchedules ...terminalRetrySchedule,
) ([]taskRunTerminalEffect, error) {
	ordinary := run.TriggerType != "restore" && run.TriggerType != "drill"
	result := make([]taskRunTerminalEffect, 0, 5)
	if ordinary && runStatus == StatusFailed && ParseStatus(taskEntity.Status) == StatusRetrying {
		retrySchedule := terminalRetrySchedule{}
		if len(retrySchedules) > 0 {
			retrySchedule = retrySchedules[0]
		}
		retryPayload := retryTaskRunEffect{
			TaskID:            taskEntity.ID,
			ChainRunID:        run.ChainRunID,
			UpstreamTaskRunID: run.UpstreamTaskRunID,
			PredecessorRunID:  run.ID,
		}
		if retrySchedule.separate {
			retryPayload.CronCursorMode = string(model.TaskRunCronCursorModeRegularV1)
		}
		payload, err := json.Marshal(retryPayload)
		if err != nil {
			return nil, fmt.Errorf("encode retry effect: %w", err)
		}
		nextAttemptAt := retrySchedule.deadline
		if nextAttemptAt == nil && !retrySchedule.separate && taskEntity.NextRunAt != nil {
			// Only legacy effects infer their deadline from Task.NextRunAt.
			// Current cron effects own the retry deadline on this effect row.
			next := taskEntity.NextRunAt.UTC()
			nextAttemptAt = &next
		}
		result = append(result, taskRunTerminalEffect{
			Key:           "retry",
			Type:          model.TaskRunEffectTypeRetry,
			Payload:       string(payload),
			NextAttemptAt: nextAttemptAt,
		})
		return result, nil
	}
	if ordinary && taskEntity.PolicyID != nil {
		eventType := ""
		switch runStatus {
		case StatusSuccess:
			eventType = automation.EventBackupSucceeded
		case StatusFailed:
			eventType = automation.EventBackupFailed
		}
		if eventType != "" {
			payload, _ := json.Marshal(automationTaskRunEffect{
				EventType: eventType,
				Context: map[string]interface{}{
					"task_id":       taskEntity.ID,
					"task_run_id":   runID,
					"policy_id":     *taskEntity.PolicyID,
					"node_id":       taskEntity.NodeID,
					"executor_type": taskEntity.ExecutorType,
					"status":        string(runStatus),
				},
			})
			result = append(result, taskRunTerminalEffect{
				Key:  "automation:" + eventType,
				Type: model.TaskRunEffectTypeAutomation, Payload: string(payload),
			})
		}
	}

	if ordinary {
		var downstreams []model.Task
		if err := tx.Where("depends_on_task_id = ?", taskEntity.ID).Order("id ASC").Find(&downstreams).Error; err != nil {
			return nil, err
		}
		for _, downstream := range downstreams {
			effectType := model.TaskRunEffectTypeDownstream
			if runStatus == StatusFailed {
				effectType = model.TaskRunEffectTypeDownstreamSkip
			} else if runStatus != StatusSuccess && runStatus != StatusWarning {
				continue
			}
			payload, _ := json.Marshal(downstreamTaskRunEffect{
				TaskID: downstream.ID, UpstreamRunID: runID, ChainRunID: run.ChainRunID,
			})
			result = append(result, taskRunTerminalEffect{
				Key:  fmt.Sprintf("downstream:%d", downstream.ID),
				Type: effectType, Payload: string(payload),
			})
		}
	}

	if ordinary {
		var alertAction string
		switch runStatus {
		case StatusSuccess:
			alertAction = "resolve"
		case StatusWarning:
			alertAction = "verification_failure"
		case StatusFailed:
			alertAction = "task_failure"
		}
		if alertAction != "" {
			message := run.LastError
			if message == "" {
				message = taskEntity.LastError
			}
			payload, _ := json.Marshal(alertTaskRunEffect{
				Action: alertAction, TaskID: taskEntity.ID, RunID: runID, Message: message,
			})
			result = append(result, taskRunTerminalEffect{
				Key:  "alert:" + alertAction,
				Type: model.TaskRunEffectTypeAlert, Payload: string(payload),
			})
		}
	}
	return result, nil
}

func persistTaskRunEffectsTx(tx *gorm.DB, runID uint, effects []taskRunTerminalEffect) error {
	if len(effects) == 0 {
		return nil
	}
	for _, effect := range effects {
		row := model.TaskRunEffect{
			TaskRunID:     runID,
			EffectKey:     effect.Key,
			EffectType:    effect.Type,
			Payload:       effect.Payload,
			Status:        model.TaskRunEffectStatusPending,
			NextAttemptAt: effect.NextAttemptAt,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

func missingRetryEffectCandidates(db *gorm.DB) *gorm.DB {
	return db.Model(&model.Task{}).
		Where("tasks.status = ?", string(StatusRetrying)).
		Where(`NOT EXISTS (
			SELECT 1
			FROM task_runs AS active_retry_runs
			WHERE active_retry_runs.task_id = tasks.id
				AND active_retry_runs.status IN ?
		)`, model.TaskRunActiveStatuses()).
		Where(`EXISTS (
			SELECT 1
			FROM task_runs AS predecessor
			WHERE predecessor.id = (
				SELECT latest.id
				FROM task_runs AS latest
				WHERE latest.task_id = tasks.id
					AND latest.trigger_type NOT IN ?
				ORDER BY latest.id DESC
				LIMIT 1
			)
			AND predecessor.status = ?
			AND NOT EXISTS (
				SELECT 1
				FROM task_run_effects AS retry_effect
				WHERE retry_effect.task_run_id = predecessor.id
					AND retry_effect.effect_type = ?
			)
		)`, []string{"drill", "restore"}, model.TaskRunStatusFailed, model.TaskRunEffectTypeRetry)
}

// reconcileMissingRetryEffects reconstructs a retry effect for legacy
// retrying Tasks that predate durable effect publication or crashed after the
// aggregate/run commit but before the effect row was inserted. The Task row is
// the serialization boundary, so concurrent startup workers cannot publish
// duplicate intent for the same latest failed predecessor.
func (m *Manager) reconcileMissingRetryEffects(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var candidates []model.Task
	if err := missingRetryEffectCandidates(m.db.WithContext(ctx)).
		Order("tasks.id ASC").
		Limit(taskRunRecoveryBatchSize).
		Find(&candidates).Error; err != nil {
		return err
	}
	for i := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.reconstructMissingRetryEffect(ctx, candidates[i].ID); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) reconstructMissingRetryEffect(ctx context.Context, taskID uint) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || ParseStatus(taskEntity.Status) != StatusRetrying {
			return nil
		}
		var activeCount int64
		if err := tx.Model(&model.TaskRun{}).
			Where("task_id = ? AND status IN ?", taskID, model.TaskRunActiveStatuses()).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount > 0 {
			return nil
		}
		var predecessor model.TaskRun
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND trigger_type NOT IN ?", taskID, []string{"drill", "restore"}).
			Order("id DESC").Limit(1).Find(&predecessor)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if predecessor.Status != model.TaskRunStatusFailed {
			return nil
		}
		var existing model.TaskRunEffect
		result = tx.Where("task_run_id = ? AND effect_type = ?", predecessor.ID, model.TaskRunEffectTypeRetry).
			Order("id ASC").Limit(1).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		payload, err := json.Marshal(retryTaskRunEffect{
			TaskID:            taskEntity.ID,
			ChainRunID:        predecessor.ChainRunID,
			UpstreamTaskRunID: predecessor.UpstreamTaskRunID,
			PredecessorRunID:  predecessor.ID,
		})
		if err != nil {
			return fmt.Errorf("encode reconstructed retry effect: %w", err)
		}
		var nextAttemptAt *time.Time
		if taskEntity.NextRunAt != nil {
			next := taskEntity.NextRunAt.UTC()
			nextAttemptAt = &next
		}
		effect := model.TaskRunEffect{
			TaskRunID: predecessor.ID, EffectKey: "retry", EffectType: model.TaskRunEffectTypeRetry,
			Payload: string(payload), Status: model.TaskRunEffectStatusPending, NextAttemptAt: nextAttemptAt,
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&effect).Error
	})
}

func (m *Manager) claimTaskRunEffect(ctx context.Context, runID uint) (*model.TaskRunEffect, error) {
	now := time.Now().UTC()
	leaseUntil := now.Add(taskRunEffectLease)
	var claimed *model.TaskRunEffect
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate model.TaskRunEffect
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_run_id = ? AND attempts < ? AND ((status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (claim_lease_until IS NULL OR claim_lease_until <= ?)))",
				runID, taskRunEffectMaxAttempts, model.TaskRunEffectStatusPending, now,
				model.TaskRunEffectStatusFailed, now, model.TaskRunEffectStatusRunning, now).
			Order("id ASC").Limit(1).Find(&candidate)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		updates := map[string]interface{}{
			"status":            model.TaskRunEffectStatusRunning,
			"claimed_by":        m.executionOwnerID,
			"claim_lease_until": &leaseUntil,
			"attempts":          candidate.Attempts + 1,
			"updated_at":        now,
		}
		updated := tx.Model(&model.TaskRunEffect{}).
			Where("id = ? AND status = ?", candidate.ID, candidate.Status).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil
		}
		candidate.Status = model.TaskRunEffectStatusRunning
		candidate.ClaimedBy = m.executionOwnerID
		candidate.ClaimLeaseUntil = &leaseUntil
		candidate.Attempts++
		claimed = &candidate
		return nil
	})
	return claimed, err
}

func (m *Manager) drainTaskRunEffects(ctx context.Context, runID uint) error {
	if m == nil || m.db == nil || runID == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	for i := 0; i < taskRunEffectBatchSize; i++ {
		effect, err := m.claimTaskRunEffect(ctx, runID)
		if err != nil {
			return err
		}
		if effect == nil {
			break
		}
		if err := m.executeTaskRunEffect(ctx, *effect); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if failErr := m.failTaskRunEffect(effect, err); failErr != nil && firstErr == nil {
				firstErr = failErr
			}
			continue
		}
		if effect.EffectType == model.TaskRunEffectTypeAutomationRule {
			if err := m.reconcilePendingDurableRuns(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := m.succeedTaskRunEffect(effect.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

func readyTaskRunEffects(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Model(&model.TaskRunEffect{}).
		Where("attempts < ? AND ((status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (claim_lease_until IS NULL OR claim_lease_until <= ?)))",
			taskRunEffectMaxAttempts, model.TaskRunEffectStatusPending, now,
			model.TaskRunEffectStatusFailed, now, model.TaskRunEffectStatusRunning, now)
}

func claimablePendingDurableRuns(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Model(&model.TaskRun{}).
		Where(`status = ? AND
			(trigger_type IN ? OR
				(trigger_type = ? AND cron_scheduled_at IS NOT NULL)) AND
			(COALESCE(execution_owner_id, '') = '' OR
				execution_lease_until IS NULL OR execution_lease_until <= ?)`,
			model.TaskRunStatusPending, []string{"auto", "retry"}, "cron", now)
}

// drainReadyTaskRunEffects claims and drains a bounded page of ready effects.
// Effects that remain not-ready are left for the next startup/tick pass.
func (m *Manager) drainReadyTaskRunEffects(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	var rows []model.TaskRunEffect
	if err := readyTaskRunEffects(m.db.WithContext(ctx), now).
		Order("id ASC").
		Limit(taskRunEffectRecoveryBatchSize()).
		Find(&rows).Error; err != nil {
		return err
	}
	seen := make(map[uint]struct{}, len(rows))
	var firstErr error
	for _, row := range rows {
		if _, ok := seen[row.TaskRunID]; ok {
			continue
		}
		seen[row.TaskRunID] = struct{}{}
		if err := m.drainTaskRunEffects(ctx, row.TaskRunID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func taskRunEffectRecoveryBatchSize() int {
	if taskRunRecoveryBatchSize > taskRunEffectBatchSize {
		return taskRunRecoveryBatchSize
	}
	return taskRunEffectBatchSize
}

func (m *Manager) executeTaskRunEffect(ctx context.Context, effect model.TaskRunEffect) error {
	switch effect.EffectType {
	case model.TaskRunEffectTypeAutomation, model.TaskRunEffectTypeAutomationRule:
		if m.autoDispatcher == nil {
			return errors.New("automation dispatcher unavailable")
		}
		var payload automationTaskRunEffect
		if effect.EffectType == model.TaskRunEffectTypeAutomation {
			if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
				return fmt.Errorf("decode automation effect: %w", err)
			}
		}
		if payload.Context == nil {
			payload.Context = make(map[string]interface{})
		}
		if effect.EffectType == model.TaskRunEffectTypeAutomationRule {
			return m.autoDispatcher.DispatchTaskRunEffect(ctx, automation.Event{}, effect)
		}
		return m.autoDispatcher.DispatchTaskRunEffect(ctx,
			automation.Event{Type: payload.EventType, Context: payload.Context}, effect)

	case model.TaskRunEffectTypeRetry:
		return m.executeRetryTaskRunEffect(ctx, effect)
	case model.TaskRunEffectTypeDownstream:
		var payload downstreamTaskRunEffect
		if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
			return fmt.Errorf("decode downstream effect: %w", err)
		}
		if payload.TaskID == 0 || payload.UpstreamRunID == 0 {
			return fmt.Errorf("downstream effect has invalid identifiers")
		}
		var existing model.TaskRun
		query := m.db.WithContext(ctx).Where("task_id = ? AND upstream_task_run_id = ?", payload.TaskID, payload.UpstreamRunID).Order("id ASC").Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 1 {
			return nil
		}
		childRunID, err := m.triggerCore(payload.TaskID, "chain", payload.ChainRunID, &payload.UpstreamRunID, nil)
		if err != nil {
			// A concurrent worker may have won the unique downstream insert.
			var raced model.TaskRun
			if lookupErr := m.db.WithContext(ctx).Where("task_id = ? AND upstream_task_run_id = ?", payload.TaskID, payload.UpstreamRunID).Limit(1).Find(&raced).Error; lookupErr == nil && raced.ID != 0 {
				return nil
			}
			return err
		}
		if childRunID != 0 {
			return nil
		}
		var child model.TaskRun
		lookup := m.db.WithContext(ctx).
			Where("task_id = ? AND upstream_task_run_id = ?", payload.TaskID, payload.UpstreamRunID).
			Limit(1).Find(&child)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected != 1 {
			return errors.New("chain dispatch produced no durable child run")
		}
		return nil

	case model.TaskRunEffectTypeDownstreamSkip:
		var payload downstreamTaskRunEffect
		if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
			return fmt.Errorf("decode downstream skip effect: %w", err)
		}
		if payload.TaskID == 0 || payload.UpstreamRunID == 0 {
			return fmt.Errorf("downstream skip effect has invalid identifiers")
		}
		var downstream model.Task
		if err := m.db.WithContext(ctx).First(&downstream, payload.TaskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		upstreamRunID := payload.UpstreamRunID
		return m.skipTask(downstream, payload.ChainRunID, &upstreamRunID, "前置任务失败，链式执行跳过")

	case model.TaskRunEffectTypeAlert:
		var payload alertTaskRunEffect
		if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
			return fmt.Errorf("decode alert effect: %w", err)
		}

		if m.alertDispatcher == nil || payload.TaskID == 0 {
			return nil
		}
		var taskEntity model.Task
		if err := m.db.WithContext(ctx).Preload("Node").Preload("Policy").First(&taskEntity, payload.TaskID).Error; err != nil {
			return err
		}
		switch payload.Action {
		case "resolve":
			return m.alertDispatcher.ResolveTaskAlertsForRun(payload.TaskID, payload.RunID, "任务恢复成功")
		case "verification_failure":
			return m.alertDispatcher.RaiseVerificationFailureForRun(taskEntity, payload.RunID, payload.Message)
		case "task_failure":
			return m.alertDispatcher.RaiseTaskFailureForRun(taskEntity, payload.RunID, payload.Message)
		default:
			return fmt.Errorf("unknown task alert effect action %q", payload.Action)
		}
	default:
		return fmt.Errorf("unknown task run effect type %q", effect.EffectType)
	}
}

func markTaskRunEffectSucceededTx(tx *gorm.DB, effectID uint, claimedBy string) error {
	if tx == nil || effectID == 0 || strings.TrimSpace(claimedBy) == "" {
		return errors.New("task run effect success transition unavailable")
	}
	now := time.Now().UTC()
	result := tx.Model(&model.TaskRunEffect{}).
		Where("id = ? AND status = ? AND claimed_by = ?", effectID, model.TaskRunEffectStatusRunning, claimedBy).
		Updates(map[string]interface{}{
			"status":            model.TaskRunEffectStatusSucceeded,
			"claimed_by":        "",
			"claim_lease_until": nil,
			"last_error":        "",
			"updated_at":        now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errTaskRunCASLost
	}
	return nil
}

func (m *Manager) executeRetryTaskRunEffect(ctx context.Context, effect model.TaskRunEffect) error {
	var payload retryTaskRunEffect
	if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
		return fmt.Errorf("decode retry effect: %w", err)
	}
	cursorMode, err := model.ParseTaskRunEffectCronCursorMode(effect.Payload)
	if err != nil {
		return fmt.Errorf("decode retry effect cron cursor mode: %w", err)
	}
	if payload.TaskID == 0 {
		return errors.New("retry effect has invalid task identifier")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var retryRunID uint
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentEffect model.TaskRunEffect
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", effect.ID).Limit(1).Find(&currentEffect)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if currentEffect.Status == model.TaskRunEffectStatusSucceeded {
			return nil
		}
		if currentEffect.Status != model.TaskRunEffectStatusRunning ||
			currentEffect.ClaimedBy != m.executionOwnerID {
			return errTaskRunCASLost
		}

		policySnapshot, err := lockTaskPolicyForFingerprint(tx, payload.TaskID)
		if err != nil {
			return err
		}
		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", payload.TaskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}
		if !taskPolicySnapshotMatches(taskEntity, policySnapshot) {
			return errTaskPolicyChanged
		}
		taskEntity.Policy = policySnapshot
		policyDisabled := policySnapshot != nil && !policySnapshot.Enabled
		// A pause, archive, policy disable, or a newer attempt supersedes a
		// retry reservation. Resolve that reservation durably instead of
		// reviving stale work.
		if taskEntity.ArchivedAt != nil || !taskEntity.Enabled || policyDisabled ||
			ParseStatus(taskEntity.Status) != StatusRetrying {
			if ParseStatus(taskEntity.Status) == StatusRetrying &&
				(taskEntity.ArchivedAt != nil || !taskEntity.Enabled || policyDisabled) {
				message := "任务已暂停，重试已取消"
				if policyDisabled {
					message = "策略已禁用，重试已取消"
				}
				updates := map[string]interface{}{
					"status":      string(StatusCanceled),
					"next_run_at": nil,
					"last_error":  message,
				}
				updated := tx.Model(&model.Task{}).
					Where("id = ? AND status = ?", taskEntity.ID, taskEntity.Status).
					Updates(updates)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return errTaskRunCASLost
				}
			}
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}
		currentCursorMode, modeErr := model.ParseTaskRunEffectCronCursorMode(currentEffect.Payload)
		if modeErr != nil {
			return fmt.Errorf("decode retry effect cron cursor mode: %w", modeErr)
		}
		cursorMode = currentCursorMode
		retryDeadline := currentEffect.NextAttemptAt
		if retryDeadline == nil && cursorMode == model.TaskRunCronCursorModeLegacy {
			// Legacy effects may not have copied their deadline onto the
			// effect row. Keep those rows compatible with Task.NextRunAt.
			retryDeadline = taskEntity.NextRunAt
		}
		if retryDeadline != nil && retryDeadline.After(time.Now().UTC()) {
			// claimTaskRunEffect applies the same readiness predicate. This
			// guard only protects against clock skew between the two reads.
			return fmt.Errorf("retry effect is not due until %s", retryDeadline.UTC().Format(time.RFC3339Nano))
		}

		if payload.PredecessorRunID == 0 || currentEffect.TaskRunID != payload.PredecessorRunID {
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}
		var latestRun model.TaskRun
		latestResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND trigger_type NOT IN ?", taskEntity.ID, []string{"drill", "restore"}).
			Order("id DESC").Limit(1).Find(&latestRun)
		if latestResult.Error != nil {
			return latestResult.Error
		}
		if latestResult.RowsAffected != 1 || latestRun.ID != payload.PredecessorRunID {
			// A newer attempt has superseded this retry cycle. Do not revive
			// the predecessor's chain after the newer attempt commits.
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}

		var activeCount int64
		if err := tx.Model(&model.TaskRun{}).
			Where("task_id = ? AND status IN ? AND id <> ?", taskEntity.ID, model.TaskRunActiveStatuses(), currentEffect.TaskRunID).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount > 0 {
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}

		chainRunID := payload.ChainRunID
		if err := validateTaskTargetOwnershipTx(tx, &taskEntity); err != nil {
			return err
		}
		if err := reservePolicySlot(tx, policySnapshot); err != nil {
			return err
		}
		if chainRunID == "" {
			var predecessor model.TaskRun
			if err := tx.Select("chain_run_id").First(&predecessor, payload.PredecessorRunID).Error; err != nil &&
				!errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			chainRunID = predecessor.ChainRunID
		}
		retryRun := model.TaskRun{
			TaskID:         taskEntity.ID,
			NodeIDSnapshot: taskEntity.NodeID,
			TriggerType:    "retry",
			Status:         model.TaskRunStatusPending,
			ChainRunID:     chainRunID,
			// The predecessor already owns this task/upstream edge. A retry
			// is another attempt in that chain, not a new dependency edge;
			// retaining the payload's upstream identity avoids reusing it in
			// the unique task/upstream index while preserving lineage.
			UpstreamTaskRunID: nil,
		}
		if err := tx.Create(&retryRun).Error; err != nil {
			return err
		}
		retryRunID = retryRun.ID
		return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
	})
	if err != nil {
		return err
	}
	if retryRunID != 0 {
		// Launch after commit. If the process exits between commit and this
		// call, the pending TaskRun remains recoverable at startup/tick.
		if launchErr := m.reconcilePendingDurableRuns(ctx); launchErr != nil {
			logger.Module("task").Warn().Uint("task_run_id", retryRunID).Err(launchErr).
				Msg("launch durable retry TaskRun")
		}
	}
	return nil
}

func isRecoverablePendingDurableRun(run model.TaskRun) bool {
	if run.Status != model.TaskRunStatusPending {
		return false
	}
	switch run.TriggerType {
	case "auto", "retry":
		return true
	case "cron":
		return run.CronScheduledAt != nil
	default:
		return false
	}
}

func (m *Manager) launchDurableTaskRun(ctx context.Context, run model.TaskRun) error {
	if m == nil || m.db == nil || run.ID == 0 || run.TaskID == 0 {
		return errors.New("durable task run launcher unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !isRecoverablePendingDurableRun(run) {
		return nil
	}
	launchCtx, ownership, claimed := m.claimPendingRunOwnership(run.TaskID)
	if !claimed {
		return nil
	}
	defer m.releasePendingRunAdmission(ownership)

	scheduled := false
	defer func() {
		if !scheduled {
			ownership.cancel()
			m.chainRunner.Delete(run.TaskID)
			m.pendingRuns.CompareAndDelete(run.TaskID, ownership)
		}
	}()

	var taskEntity model.Task
	if result := m.db.WithContext(ctx).Preload("Policy").Where("id = ?", run.TaskID).Limit(1).Find(&taskEntity); result.Error != nil {
		return result.Error
	} else if result.RowsAffected != 1 {
		return m.cancelPendingDurableRun(ctx, run.ID, run.TaskID, "关联任务不存在")
	}
	var currentRun model.TaskRun
	if result := m.db.WithContext(ctx).Where("id = ? AND task_id = ?", run.ID, run.TaskID).
		Limit(1).Find(&currentRun); result.Error != nil {
		return result.Error
	} else if result.RowsAffected != 1 || !isRecoverablePendingDurableRun(currentRun) {
		return nil
	}
	run = currentRun
	if taskEntity.ArchivedAt != nil || !taskEntity.Enabled ||
		(taskEntity.Policy != nil && !taskEntity.Policy.Enabled) {
		return m.cancelPendingDurableRun(ctx, run.ID, run.TaskID, "任务已暂停，重试或自动触发已取消")
	}
	runCtx, runCancel := m.newRunContext(launchCtx, computeExecTimeout(taskEntity))
	ownership.addCancel(runCancel)
	if err := runCtx.Err(); err != nil {
		return err
	}
	if !m.handoffPendingRunAdmission(ownership) {
		if !m.shuttingDown.Load() {
			if err := m.cancelTaskRunBeforeExecutorWithOwnership(
				run.TaskID, run.ID, "任务执行入口未获准", ownership,
			); err != nil {
				return err
			}
		}
		return nil
	}
	scheduled = true
	go func() {
		defer m.taskWG.Done()
		m.runTaskWithContext(run.TaskID, run.ID, run.TriggerType, run.ChainRunID, runCtx, ownership, ownership.cancel)
	}()
	return nil
}
func (m *Manager) reconcilePendingDurableRuns(ctx context.Context) error {
	if m == nil || m.db == nil || m.shuttingDown.Load() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.pendingRecoveryMu.Lock()
	defer m.pendingRecoveryMu.Unlock()

	loadRuns := func(afterID uint) ([]model.TaskRun, error) {
		query := claimablePendingDurableRuns(m.db.WithContext(ctx), time.Now().UTC())
		if afterID != 0 {
			query = query.Where("id > ?", afterID)
		}
		var runs []model.TaskRun
		if err := query.Order("id ASC").Limit(taskRunRecoveryBatchSize).Find(&runs).Error; err != nil {
			return nil, err
		}
		return runs, nil
	}

	cursor := m.pendingRecoveryCursor
	runs, err := loadRuns(cursor)
	if err != nil {
		return err
	}
	if len(runs) == 0 && cursor != 0 {
		runs, err = loadRuns(0)
		if err != nil {
			return err
		}
	}
	var firstErr error
	for _, run := range runs {
		if _, alreadyHandled := m.pendingRuns.Load(run.TaskID); alreadyHandled {
			continue
		}
		if err := m.launchDurableTaskRun(ctx, run); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if len(runs) == 0 {
		m.pendingRecoveryCursor = 0
		return nil
	}
	m.pendingRecoveryCursor = runs[len(runs)-1].ID
	return nil
}

func (m *Manager) cancelPendingDurableRun(ctx context.Context, runID, taskID uint, message string) error {
	now := time.Now().UTC()
	if ctx == nil {
		ctx = context.Background()
	}
	legacyReanchor := false
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", taskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil && !errors.Is(taskResult.Error, gorm.ErrRecordNotFound) {
			return taskResult.Error
		}
		var run model.TaskRun
		runResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND task_id = ? AND status = ?", runID, taskID, model.TaskRunStatusPending).
			Limit(1).Find(&run)
		if runResult.Error != nil {
			return runResult.Error
		}
		if runResult.RowsAffected != 1 {
			return nil
		}
		owner := strings.TrimSpace(run.ExecutionOwnerID)
		if owner != "" && owner != m.executionOwnerID &&
			(run.ExecutionLeaseUntil != nil && run.ExecutionLeaseUntil.After(now)) {
			return errTaskRunNotOwner
		}

		retryCursorMode := model.TaskRunCronCursorModeLegacy
		if taskResult.RowsAffected == 1 &&
			strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") &&
			ParseStatus(taskEntity.Status) == StatusRetrying {
			var modeErr error
			retryCursorMode, modeErr = retryCursorModeForTerminalRunTx(tx, taskEntity, run)
			if modeErr != nil {
				return modeErr
			}
		}

		updated := tx.Model(&model.TaskRun{}).
			Where(`id = ? AND task_id = ? AND status = ? AND
				(execution_owner_id = '' OR execution_owner_id = ? OR
					execution_lease_until IS NULL OR execution_lease_until <= ?)`,
				runID, taskID, model.TaskRunStatusPending, m.executionOwnerID, now).
			Updates(map[string]interface{}{
				"status":                model.TaskRunStatusCanceled,
				"finished_at":           &now,
				"last_error":            sanitizeTaskLastError(message),
				"execution_owner_id":    "",
				"execution_lease_until": nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		if taskResult.RowsAffected == 1 &&
			strings.EqualFold(strings.TrimSpace(run.TriggerType), "retry") &&
			ParseStatus(taskEntity.Status) == StatusRetrying {
			taskUpdates := map[string]interface{}{
				"status":     string(StatusCanceled),
				"last_error": sanitizeTaskLastError(message),
			}
			if strings.TrimSpace(taskEntity.CronSpec) == "" ||
				!taskEntity.Enabled || taskEntity.ArchivedAt != nil {
				taskUpdates["next_run_at"] = nil
			} else if retryCursorMode != model.TaskRunCronCursorModeRegularV1 {
				next := cronutil.Next(taskEntity.CronSpec)
				taskUpdates["next_run_at"] = next
				legacyReanchor = next != nil
			}
			taskUpdated := tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", taskID, taskEntity.Status).
				Updates(taskUpdates)
			if taskUpdated.Error != nil {
				return taskUpdated.Error
			}
			if taskUpdated.RowsAffected != 1 {
				return errTaskRunCASLost
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if legacyReanchor {
		if err := m.SyncSchedule(model.Task{ID: taskID}); err != nil {
			return fmt.Errorf("reconcile legacy retry schedule after cancellation: %w", err)
		}
	}
	return nil

}

func (m *Manager) succeedTaskRunEffect(effectID uint) error {
	now := time.Now().UTC()
	result := m.db.Model(&model.TaskRunEffect{}).
		Where("id = ? AND status = ? AND claimed_by = ?", effectID, model.TaskRunEffectStatusRunning, m.executionOwnerID).
		Updates(map[string]interface{}{
			"status":            model.TaskRunEffectStatusSucceeded,
			"claimed_by":        "",
			"claim_lease_until": nil,
			"last_error":        "",
			"updated_at":        now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	// A transactional effect executor (retry/automation rule) may publish
	// success together with its side effects. Treat the outer completion CAS as
	// idempotent for that already-committed state.
	var current model.TaskRunEffect
	lookup := m.db.Where("id = ?", effectID).Limit(1).Find(&current)
	if lookup.Error != nil {
		return lookup.Error
	}
	if lookup.RowsAffected == 1 && current.Status == model.TaskRunEffectStatusSucceeded {
		return nil
	}
	return errTaskRunCASLost
}

func (m *Manager) failTaskRunEffect(effect *model.TaskRunEffect, effectErr error) error {
	if effect == nil {
		return nil
	}
	if effectErr == nil {
		effectErr = errors.New("task run effect failed")
	}
	now := time.Now().UTC()
	message := sanitizeTaskLastError(effectErr.Error())
	var modeErr error
	var cursorMode model.TaskRunCronCursorMode
	err := m.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		var current model.TaskRunEffect
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", effect.ID).Limit(1).Find(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if current.Status == model.TaskRunEffectStatusSucceeded {
			return nil
		}
		if current.Status != model.TaskRunEffectStatusRunning ||
			current.ClaimedBy != m.executionOwnerID {
			return errTaskRunCASLost
		}
		if current.EffectType == model.TaskRunEffectTypeRetry {
			cursorMode, modeErr = model.ParseTaskRunEffectCronCursorMode(current.Payload)
		}
		attempt := current.Attempts
		if attempt < 1 {
			attempt = 1
		}
		backoff := time.Second * time.Duration(1<<uint(minInt(attempt-1, 8)))
		if backoff > taskRunEffectBackoffLimit {
			backoff = taskRunEffectBackoffLimit
		}
		updates := map[string]interface{}{
			"status":            model.TaskRunEffectStatusFailed,
			"claimed_by":        "",
			"claim_lease_until": nil,
			"last_error":        message,
			"updated_at":        now,
		}
		if current.Attempts >= taskRunEffectMaxAttempts {
			updates["next_attempt_at"] = nil
		} else {
			next := now.Add(backoff)
			updates["next_attempt_at"] = &next
		}
		updated := tx.Model(&model.TaskRunEffect{}).
			Where("id = ? AND status = ? AND claimed_by = ?", current.ID, model.TaskRunEffectStatusRunning, m.executionOwnerID).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		if modeErr != nil && current.Attempts < taskRunEffectMaxAttempts {
			// The effect failure is durable, but an invalid provenance value
			// must never be interpreted as a legacy cron deadline. Keep the
			// retry reservation for bounded recovery; exhaustion settles it.
			return nil
		}

		if current.Attempts < taskRunEffectMaxAttempts ||
			current.EffectType != model.TaskRunEffectTypeRetry {
			return nil
		}
		var source model.TaskRun
		sourceResult := tx.Where("id = ?", current.TaskRunID).Limit(1).Find(&source)
		if sourceResult.Error != nil {
			return sourceResult.Error
		}
		if sourceResult.RowsAffected != 1 {
			return nil
		}
		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", source.TaskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 || ParseStatus(taskEntity.Status) != StatusRetrying {
			return nil
		}
		var latestRun model.TaskRun
		latestResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND trigger_type NOT IN ?", taskEntity.ID, []string{"drill", "restore"}).
			Order("id DESC").Limit(1).Find(&latestRun)
		if latestResult.Error != nil {
			return latestResult.Error
		}
		if latestResult.RowsAffected != 1 || latestRun.ID != current.TaskRunID {
			// A newer predecessor owns the current retry cycle. The exhausted
			// stale effect must not fail that newer cycle's aggregate.
			return nil
		}
		taskUpdates := map[string]interface{}{
			"status":     string(StatusFailed),
			"last_error": message,
		}
		// Current cron retry effects keep the regular cursor on Task. Legacy
		// effects still need the historical completion-based cursor reset.
		if modeErr != nil {
			// Unknown provenance has no safe regular/legacy interpretation.
			// Clearing the cursor creates an explicit boundary for a later
			// schedule reconstruction instead of replaying an old timestamp.
			taskUpdates["next_run_at"] = nil
		} else if strings.TrimSpace(taskEntity.CronSpec) == "" ||
			cursorMode != model.TaskRunCronCursorModeRegularV1 {
			taskUpdates["next_run_at"] = cronutil.Next(taskEntity.CronSpec)
		}
		taskUpdated := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", taskEntity.ID, taskEntity.Status).
			Updates(taskUpdates)
		if taskUpdated.Error != nil {
			return taskUpdated.Error
		}
		if taskUpdated.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		return nil
	})
	if err != nil {
		return err
	}
	if modeErr != nil {
		return fmt.Errorf("decode retry effect cron cursor mode: %w", modeErr)
	}
	return nil

}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// claimTaskRunOwner establishes a durable lease before any executor work. An
// active lease owned by another process is never overwritten. Expired leases
// are claimable by one process through a conditional update.
func (m *Manager) claimTaskRunOwner(ctx context.Context, taskID, runID uint) (bool, error) {
	if m == nil || m.db == nil || runID == 0 {
		return false, errors.New("task run owner unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(m.taskRunLeaseDuration())
	result := m.db.WithContext(ctx).Model(&model.TaskRun{}).
		Where("id = ? AND task_id = ? AND status IN ? AND (execution_owner_id = '' OR execution_owner_id = ? OR execution_lease_until IS NULL OR execution_lease_until <= ?)",
			runID, taskID, []string{model.TaskRunStatusPending, model.TaskRunStatusRunning}, m.executionOwnerID, now).
		Updates(map[string]interface{}{
			"execution_owner_id":    m.executionOwnerID,
			"execution_lease_until": &leaseUntil,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	return false, nil
}

func (m *Manager) renewTaskRunOwner(ctx context.Context, runID uint) error {
	if m == nil || m.db == nil || runID == 0 || m.executionOwnerID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseUntil := time.Now().UTC().Add(m.taskRunLeaseDuration())
	result := m.db.WithContext(ctx).Model(&model.TaskRun{}).
		Where("id = ? AND execution_owner_id = ? AND status IN ?", runID, m.executionOwnerID, model.TaskRunActiveStatuses()).
		Update("execution_lease_until", &leaseUntil)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errTaskRunNotOwner
	}
	return nil
}

func (m *Manager) taskRunLeaseDuration() time.Duration {
	if m == nil || m.executionLeaseDuration <= 0 {
		return defaultTaskRunLease
	}
	return m.executionLeaseDuration
}

func (m *Manager) taskRunRenewalTimeout() time.Duration {
	timeout := m.taskRunLeaseDuration() / 2
	if timeout <= 0 {
		timeout = time.Millisecond
	}
	const maxRenewalTimeout = 5 * time.Second
	if timeout > maxRenewalTimeout {
		timeout = maxRenewalTimeout
	}
	return timeout
}

func (m *Manager) startTaskRunHeartbeat(
	ctx context.Context,
	runID uint,
	cancelRun context.CancelFunc,
) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	if cancelRun == nil {
		cancelRun = func() {}
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	interval := m.taskRunLeaseDuration() / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	if !m.addTaskWorker() {
		cancelHeartbeat()
		return cancelHeartbeat
	}
	go func() {
		defer m.taskWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(heartbeatCtx, m.taskRunRenewalTimeout())
				err := m.renewTaskRunOwner(renewCtx, runID)
				renewCancel()
				if err != nil {
					logger.Module("task").Warn().Uint("task_run_id", runID).Err(err).Msg("renew task run execution lease")
					// Ownership loss is an executor cancellation, not a
					// best-effort heartbeat warning. The runner must return
					// before another process can publish recovery effects.
					cancelRun()
					return
				}
			}
		}
	}()
	return cancelHeartbeat
}

func (m *Manager) reconcileExpiredOrdinaryRuns(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	staleBefore := now.Add(-taskRunRecoveryGrace)
	var candidates []model.TaskRun
	query := m.db.WithContext(ctx).
		Where(`trigger_type <> ? AND status IN ? AND
			NOT (status = ? AND
				(trigger_type IN ? OR
					(trigger_type = ? AND cron_scheduled_at IS NOT NULL))) AND
			((execution_lease_until IS NOT NULL AND execution_lease_until <= ?) OR
				(execution_owner_id = '' AND updated_at <= ?))`,
			"drill", model.TaskRunActiveStatuses(), model.TaskRunStatusPending,
			[]string{"auto", "retry"}, "cron", now, staleBefore).
		Order("id ASC").Limit(taskRunRecoveryBatchSize)
	if err := query.Find(&candidates).Error; err != nil {
		return err
	}
	for i := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		run := candidates[i]
		if m.chainRunner != nil {
			if _, live := m.chainRunner.Load(run.TaskID); live {
				// A same-process runner still owns the executor context. Let
				// its cancellation/recovery path publish the terminal result;
				// another goroutine must not race it after lease expiry.
				continue
			}
		}
		claimed, err := m.claimTaskRunOwner(ctx, run.TaskID, run.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := m.reconcileClaimedOrdinaryRun(ctx, run); err != nil {
			return err
		}
	}
	if len(candidates) == taskRunRecoveryBatchSize {
		var remaining int64
		if err := m.db.WithContext(ctx).Model(&model.TaskRun{}).
			Where(`trigger_type <> ? AND status IN ? AND
				NOT (status = ? AND
					(trigger_type IN ? OR
						(trigger_type = ? AND cron_scheduled_at IS NOT NULL))) AND
				((execution_lease_until IS NOT NULL AND execution_lease_until <= ?) OR
					(execution_owner_id = '' AND updated_at <= ?))`,
				"drill", model.TaskRunActiveStatuses(), model.TaskRunStatusPending,
				[]string{"auto", "retry"}, "cron", now, staleBefore).Count(&remaining).Error; err != nil {
			return err
		}
		if remaining > 0 {
			return fmt.Errorf("ordinary task run recovery backlog exceeds bounded pass")
		}
	}
	return nil
}

func (m *Manager) reconcileClaimedOrdinaryRun(ctx context.Context, run model.TaskRun) error {
	var taskEntity model.Task
	if err := m.db.WithContext(ctx).First(&taskEntity, run.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	status := ParseStatus(taskEntity.Status)
	var taskStatus *TaskStatus
	if status == StatusRunning || status == StatusRetrying {
		failed := StatusFailed
		taskStatus = &failed
	}
	message := "任务进程中断，已自动恢复"
	finishedAt := time.Now().UTC()
	runUpdates := map[string]interface{}{
		"finished_at": &finishedAt,
		"last_error":  message,
	}
	if run.StartedAt != nil {
		duration := finishedAt.Sub(run.StartedAt.UTC()).Milliseconds()
		if duration > 0 {
			runUpdates["duration_ms"] = duration
		}
	}
	return m.terminalizeTaskRun(ctx, run.TaskID, run.ID, []string{run.Status}, taskStatus,
		map[string]interface{}{"last_error": message, "next_run_at": cronutil.Next(taskEntity.CronSpec)},
		StatusFailed, runUpdates)
}

// recoverTaskRunOnReturn is used by the runner's deferred crash guard. It is
// owner-fenced and updates the aggregate and run atomically whenever the
// runner exits before an explicit terminal transition.
func (m *Manager) recoverTaskRunOnReturn(ctx context.Context, taskID, runID uint, message string) error {
	now := time.Now().UTC()
	return m.terminalizeTaskRunTx(ctx, taskID, runID, model.TaskRunActiveStatuses(), nil,
		map[string]interface{}{"last_error": message},
		StatusFailed, map[string]interface{}{"finished_at": &now, "last_error": message},
		terminalizeTaskRunModeRecovery)
}
func (m *Manager) terminalizeRestoreTaskRun(ctx context.Context, taskID, runID uint, expectedStatuses []string, runStatus TaskStatus, updates map[string]interface{}) error {
	return m.terminalizeTaskRun(ctx, taskID, runID, expectedStatuses, nil, nil, runStatus, updates)
}

func (m *Manager) recoverRestoreTaskRunOnReturn(ctx context.Context, taskID, runID uint, message string) error {
	now := time.Now().UTC()
	return m.terminalizeRestoreTaskRun(ctx, taskID, runID, model.TaskRunActiveStatuses(), StatusFailed,
		map[string]interface{}{"finished_at": &now, "last_error": message})
}
