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
		if strings.TrimSpace(run.ExecutionOwnerID) != "" &&
			m.executionOwnerID != "" && run.ExecutionOwnerID != m.executionOwnerID &&
			(run.ExecutionLeaseUntil == nil || run.ExecutionLeaseUntil.After(now)) {
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
	Key     string
	Type    string
	Payload string
}

type automationTaskRunEffect struct {
	EventType string                 `json:"event_type"`
	Context   map[string]interface{} `json:"context"`
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
		if strings.TrimSpace(run.ExecutionOwnerID) != "" &&
			m.executionOwnerID != "" && run.ExecutionOwnerID != m.executionOwnerID {
			if run.ExecutionLeaseUntil == nil || run.ExecutionLeaseUntil.After(finishedAt) {
				return errTaskRunNotOwner
			}
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
	result := make([]taskRunTerminalEffect, 0, 4)
	if ordinary && taskEntity.PolicyID != nil {
		eventType := ""
		switch runStatus {
		case StatusSuccess, StatusWarning, StatusFailed:
			eventType = automation.EventBackupSucceeded
			if runStatus != StatusSuccess {
				eventType = automation.EventBackupFailed
			}
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
			TaskRunID: runID, EffectKey: effect.Key, EffectType: effect.Type,
			Payload: effect.Payload, Status: model.TaskRunEffectStatusPending,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
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
			m.failTaskRunEffect(effect, err)
			continue
		}
		if err := m.succeedTaskRunEffect(effect.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m *Manager) drainReadyTaskRunEffects(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	var rows []model.TaskRunEffect
	if err := m.db.WithContext(ctx).
		Where("attempts < ? AND ((status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)) OR (status = ? AND (claim_lease_until IS NULL OR claim_lease_until <= ?)))",
			taskRunEffectMaxAttempts, model.TaskRunEffectStatusPending, now,
			model.TaskRunEffectStatusFailed, now, model.TaskRunEffectStatusRunning, now).
		Order("id ASC").Limit(taskRunEffectRecoveryBatchSize()).Find(&rows).Error; err != nil {
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
	if len(rows) == taskRunEffectRecoveryBatchSize() {
		return fmt.Errorf("task terminal effect recovery backlog exceeds bounded pass")
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
	case model.TaskRunEffectTypeAutomation:
		var payload automationTaskRunEffect
		if err := json.Unmarshal([]byte(effect.Payload), &payload); err != nil {
			return fmt.Errorf("decode automation effect: %w", err)
		}
		if m.autoDispatcher == nil {
			return nil
		}
		if payload.Context == nil {
			payload.Context = make(map[string]interface{})
		}
		payload.Context["_effect_key"] = effectKeyForRun(effect)
		return m.autoDispatcher.Dispatch(ctx, automation.Event{Type: payload.EventType, Context: payload.Context})

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

func effectKeyForRun(effect model.TaskRunEffect) string {
	return fmt.Sprintf("task_run:%d/effect:%d/%s", effect.TaskRunID, effect.ID, effect.EffectKey)
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
	if result.RowsAffected != 1 {
		return errTaskRunCASLost
	}
	return nil
}

func (m *Manager) failTaskRunEffect(effect *model.TaskRunEffect, effectErr error) {
	if effect == nil {
		return
	}
	now := time.Now().UTC()
	attempt := effect.Attempts
	if attempt < 1 {
		attempt = 1
	}
	backoff := time.Second * time.Duration(1<<uint(minInt(attempt-1, 8)))
	if backoff > taskRunEffectBackoffLimit {
		backoff = taskRunEffectBackoffLimit
	}
	next := now.Add(backoff)
	message := sanitizeTaskLastError(effectErr.Error())
	_ = m.db.Model(&model.TaskRunEffect{}).
		Where("id = ? AND status = ? AND claimed_by = ?", effect.ID, model.TaskRunEffectStatusRunning, m.executionOwnerID).
		Updates(map[string]interface{}{
			"status":            model.TaskRunEffectStatusFailed,
			"next_attempt_at":   &next,
			"claimed_by":        "",
			"claim_lease_until": nil,
			"last_error":        message,
			"updated_at":        now,
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

func (m *Manager) renewTaskRunOwner(runID uint) error {
	if m == nil || m.db == nil || runID == 0 || m.executionOwnerID == "" {
		return nil
	}
	leaseUntil := time.Now().UTC().Add(m.taskRunLeaseDuration())
	result := m.db.Model(&model.TaskRun{}).
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

func (m *Manager) startTaskRunHeartbeat(ctx context.Context, runID uint) context.CancelFunc {
	if ctx == nil {
		ctx = context.Background()
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	interval := m.taskRunLeaseDuration() / 3
	if interval < time.Second {
		interval = time.Second
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
				if err := m.renewTaskRunOwner(runID); err != nil {
					logger.Module("task").Warn().Uint("task_run_id", runID).Err(err).Msg("renew task run execution lease")
					return
				}
			}
		}
	}()
	return cancel
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
