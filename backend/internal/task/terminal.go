package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/automation"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	var effects []taskRunTerminalEffect
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.TaskRun
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", runID).Limit(1).Find(&run)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
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
		updates := map[string]interface{}{
			"status":                model.TaskRunStatusFailed,
			"finished_at":           &now,
			"last_error":            sanitizeTaskLastError(message),
			"execution_owner_id":    "",
			"execution_lease_until": nil,
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
		run.LastError = updates["last_error"].(string)
		result = tx.Model(&model.TaskRun{}).
			Where("id = ? AND status = ?", run.ID, run.Status).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTaskRunCASLost
		}
		var taskEntity model.Task
		taskResult := tx.Where("id = ?", run.TaskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected == 1 {
			var buildErr error
			effects, buildErr = buildTerminalEffects(tx, taskEntity, run, run.ID, StatusFailed)
			if buildErr != nil {
				return buildErr
			}
			return persistTaskRunEffectsTx(tx, run.ID, effects)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.drainTaskRunEffects(ctx, runID)
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
func (m *Manager) terminalizeTaskRun(
	ctx context.Context,
	taskID, runID uint,
	expectedRunStatuses []string,
	taskStatus *TaskStatus,
	taskUpdates map[string]interface{},
	runStatus TaskStatus,
	runUpdates map[string]interface{},
) error {
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
		builtEffects, buildErr := buildTerminalEffects(tx, taskEntity, run, runID, runStatus)
		if buildErr != nil {
			return buildErr
		}
		effects = builtEffects
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
func buildTerminalEffects(tx *gorm.DB, taskEntity model.Task, run model.TaskRun, runID uint, runStatus TaskStatus) ([]taskRunTerminalEffect, error) {
	ordinary := run.TriggerType != "restore" && run.TriggerType != "drill"
	result := make([]taskRunTerminalEffect, 0, 5)
	if ordinary && runStatus == StatusFailed && ParseStatus(taskEntity.Status) == StatusRetrying {
		payload, err := json.Marshal(retryTaskRunEffect{
			TaskID:            taskEntity.ID,
			ChainRunID:        run.ChainRunID,
			UpstreamTaskRunID: run.UpstreamTaskRunID,
			PredecessorRunID:  run.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("encode retry effect: %w", err)
		}
		var nextAttemptAt *time.Time
		if taskEntity.NextRunAt != nil {
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
				ORDER BY latest.id DESC
				LIMIT 1
			)
			AND predecessor.status = ?
			AND predecessor.trigger_type NOT IN ?
			AND NOT EXISTS (
				SELECT 1
				FROM task_run_effects AS retry_effect
				WHERE retry_effect.task_run_id = predecessor.id
					AND retry_effect.effect_type = ?
			)
		)`, model.TaskRunStatusFailed, []string{"drill", "restore"}, model.TaskRunEffectTypeRetry)
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
			Where("task_id = ?", taskID).
			Order("id DESC").Limit(1).Find(&predecessor)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if predecessor.Status != model.TaskRunStatusFailed ||
			predecessor.TriggerType == "drill" || predecessor.TriggerType == "restore" {
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
		Where(`status = ? AND trigger_type IN ? AND
			(COALESCE(execution_owner_id, '') = '' OR
				execution_lease_until IS NULL OR execution_lease_until <= ?)`,
			model.TaskRunStatusPending, []string{"auto", "retry"}, now)
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
		if _, err := m.triggerCore(payload.TaskID, "chain", payload.ChainRunID, &payload.UpstreamRunID); err != nil {
			// A concurrent worker may have won the unique downstream insert.
			var raced model.TaskRun
			if lookupErr := m.db.WithContext(ctx).Where("task_id = ? AND upstream_task_run_id = ?", payload.TaskID, payload.UpstreamRunID).Limit(1).Find(&raced).Error; lookupErr == nil && raced.ID != 0 {
				return nil
			}
			return err
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
		runID := payload.RunID
		runIDPtr := &runID
		switch payload.Action {
		case "resolve":
			return m.alertDispatcher.ResolveTaskAlerts(payload.TaskID, "任务恢复成功")
		case "verification_failure":
			return m.alertDispatcher.RaiseVerificationFailure(taskEntity, runIDPtr, payload.Message)
		case "task_failure":
			return m.alertDispatcher.RaiseTaskFailure(taskEntity, runIDPtr, payload.Message)
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
	if payload.TaskID == 0 {
		return errors.New("retry effect has invalid task identifier")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var retryRunID uint
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

		var taskEntity model.Task
		taskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", payload.TaskID).Limit(1).Find(&taskEntity)
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return markTaskRunEffectSucceededTx(tx, currentEffect.ID, m.executionOwnerID)
		}
		policyDisabled := false
		if taskEntity.PolicyID != nil {
			var policyEntity model.Policy
			policyResult := tx.Where("id = ?", *taskEntity.PolicyID).Limit(1).Find(&policyEntity)
			if policyResult.Error != nil {
				return policyResult.Error
			}
			policyDisabled = policyResult.RowsAffected == 1 && !policyEntity.Enabled
		}
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
		if taskEntity.NextRunAt != nil && taskEntity.NextRunAt.After(time.Now().UTC()) {
			// claimTaskRunEffect applies the same readiness predicate. This
			// guard only protects against clock skew between the two reads.
			return fmt.Errorf("retry effect is not due until %s", taskEntity.NextRunAt.UTC().Format(time.RFC3339Nano))
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

func (m *Manager) launchDurableTaskRun(ctx context.Context, run model.TaskRun) error {
	if m == nil || m.db == nil || run.ID == 0 || run.TaskID == 0 {
		return errors.New("durable task run launcher unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	launchCtx, ownership, claimed := m.claimPendingRunOwnership(run.TaskID)
	if !claimed {
		return nil
	}
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
	} else if result.RowsAffected != 1 || currentRun.Status != model.TaskRunStatusPending {
		return nil
	}
	if taskEntity.ArchivedAt != nil || !taskEntity.Enabled ||
		(taskEntity.Policy != nil && !taskEntity.Policy.Enabled) {
		return m.cancelPendingDurableRun(ctx, run.ID, run.TaskID, "任务已暂停，重试或自动触发已取消")
	}
	runCtx, runCancel := m.newRunContext(launchCtx, computeExecTimeout(taskEntity))
	ownership.addCancel(runCancel)
	if err := runCtx.Err(); err != nil {
		return err
	}
	scheduled = true
	m.taskWG.Add(1)
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
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if strings.TrimSpace(run.ExecutionOwnerID) != "" &&
			run.ExecutionOwnerID != m.executionOwnerID {
			return errTaskRunNotOwner
		}
		updated := tx.Model(&model.TaskRun{}).
			Where(`id = ? AND task_id = ? AND status = ? AND
				(execution_owner_id = '' OR execution_owner_id = ?)`,
				runID, taskID, model.TaskRunStatusPending, m.executionOwnerID).
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
		if taskResult.RowsAffected == 1 && run.TriggerType == "retry" &&
			ParseStatus(taskEntity.Status) == StatusRetrying {
			taskUpdated := tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", taskID, taskEntity.Status).
				Updates(map[string]interface{}{
					"status":      string(StatusCanceled),
					"next_run_at": nil,
					"last_error":  sanitizeTaskLastError(message),
				})
			if taskUpdated.Error != nil {
				return taskUpdated.Error
			}
			if taskUpdated.RowsAffected != 1 {
				return errTaskRunCASLost
			}
		}
		return nil
	})
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
	return m.db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
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
			"status":      string(StatusFailed),
			"next_run_at": nextCronRun(taskEntity.CronSpec),
			"last_error":  message,
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
	m.taskWG.Add(1)
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
		Where("trigger_type <> ? AND status IN ? AND ((execution_lease_until IS NOT NULL AND execution_lease_until <= ?) OR (execution_owner_id = '' AND updated_at <= ?))",
			"drill", model.TaskRunActiveStatuses(), now, staleBefore).
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
			Where("trigger_type <> ? AND status IN ? AND ((execution_lease_until IS NOT NULL AND execution_lease_until <= ?) OR (execution_owner_id = '' AND updated_at <= ?))",
				"drill", model.TaskRunActiveStatuses(), now, staleBefore).Count(&remaining).Error; err != nil {
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
		map[string]interface{}{"last_error": message, "next_run_at": nextCronRun(taskEntity.CronSpec)},
		StatusFailed, runUpdates)
}

// recoverTaskRunOnReturn is used by the runner's deferred crash guard. It is
// owner-fenced and updates the aggregate and run atomically whenever the
// runner exits before an explicit terminal transition.
func (m *Manager) recoverTaskRunOnReturn(ctx context.Context, taskID, runID uint, message string) error {
	var taskEntity model.Task
	if err := m.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		return err
	}
	status := ParseStatus(taskEntity.Status)
	var taskStatus *TaskStatus
	if status == StatusRunning || status == StatusRetrying {
		failed := StatusFailed
		taskStatus = &failed
	}
	now := time.Now().UTC()
	return m.terminalizeTaskRun(ctx, taskID, runID, model.TaskRunActiveStatuses(), taskStatus,
		map[string]interface{}{"last_error": message, "next_run_at": nextCronRun(taskEntity.CronSpec)},
		StatusFailed, map[string]interface{}{"finished_at": &now, "last_error": message})
}
func (m *Manager) terminalizeRestoreTaskRun(ctx context.Context, taskID, runID uint, expectedStatuses []string, runStatus TaskStatus, updates map[string]interface{}) error {
	return m.terminalizeTaskRun(ctx, taskID, runID, expectedStatuses, nil, nil, runStatus, updates)
}

func (m *Manager) recoverRestoreTaskRunOnReturn(ctx context.Context, taskID, runID uint, message string) error {
	now := time.Now().UTC()
	return m.terminalizeRestoreTaskRun(ctx, taskID, runID, model.TaskRunActiveStatuses(), StatusFailed,
		map[string]interface{}{"finished_at": &now, "last_error": message})
}
