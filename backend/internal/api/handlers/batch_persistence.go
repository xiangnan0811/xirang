package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"xirang/backend/internal/dbtx"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type batchCommandRequest struct {
	NodeIDs []uint `json:"node_ids" binding:"required,min=1"`
	Command string `json:"command" binding:"required"`
	Name    string `json:"name"`
	Retain  bool   `json:"retain"`
}

type batchNode struct {
	ID   uint
	Name string
}

type batchRetrySource struct {
	TaskID uint `json:"task_id"`
	RunID  uint `json:"run_id"`
}

var errBatchIdempotencyConflict = errors.New("batch idempotency conflict")
var errBatchDeleted = errors.New("batch already deleted")
var errBatchActive = errors.New("batch still has active dispatch or execution effects")
var errBatchRetryAlreadyClaimed = errors.New("batch retry source already claimed")

func insertBatchCommandTasksTx(tx *gorm.DB, batch model.BatchCommand, req batchCommandRequest, nodes []batchNode, sources []batchRetrySource) ([]model.BatchCommandDispatch, error) {
	if sources != nil && len(sources) != len(nodes) {
		return nil, fmt.Errorf("batch retry sources cardinality mismatch: got %d sources for %d nodes", len(sources), len(nodes))
	}

	name := req.Name
	if name == "" {
		name = "批量命令 " + batch.ID
	}
	dispatches := make([]model.BatchCommandDispatch, 0, len(nodes))
	for index, node := range nodes {
		task := model.Task{
			Name:         fmt.Sprintf("%s [%s]", name, node.Name),
			NodeID:       node.ID,
			ExecutorType: "command",
			Command:      req.Command,
			Source:       "batch",
			Status:       "pending",
			BatchID:      batch.ID,
		}
		if err := tx.Create(&task).Error; err != nil {
			return nil, err
		}
		commandFingerprint := model.BatchCommandTaskFingerprint(task)
		if commandFingerprint == "" {
			return nil, errors.New("batch command task fingerprint is empty")
		}
		dispatch := model.BatchCommandDispatch{
			TaskID:             task.ID,
			BatchID:            batch.ID,
			NodeID:             node.ID,
			Status:             "pending",
			CommandFingerprint: commandFingerprint,
		}
		if sources == nil {
			if err := tx.Create(&dispatch).Error; err != nil {
				return nil, err
			}
		} else {
			retryOfTaskID := sources[index].TaskID
			retryOfRunID := sources[index].RunID
			dispatch.RetryOfTaskID = &retryOfTaskID
			dispatch.RetryOfRunID = &retryOfRunID
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "retry_of_task_id"},
					{Name: "retry_of_run_id"},
				},
				DoNothing: true,
			}).Create(&dispatch)
			if result.Error != nil {
				return nil, result.Error
			}
			if result.RowsAffected == 0 {
				return nil, errBatchRetryAlreadyClaimed
			}
		}
		dispatches = append(dispatches, dispatch)
	}
	return dispatches, nil
}

func (h *BatchHandler) createBatchTasks(ctx context.Context, actorID uint, key string, req batchCommandRequest, nodes []batchNode) (model.BatchCommand, []model.BatchCommandDispatch, bool, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return model.BatchCommand{}, nil, false, err
	}
	hash := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(hash[:])
	var batch model.BatchCommand
	var dispatches []model.BatchCommandDispatch
	created := false
	err = dbtx.WithSQLiteBusyRetryTx(ctx, h.db, func(tx *gorm.DB) error {
		// A retry owns a fresh candidate and slice: rolled-back IDs are not state.
		batch = model.BatchCommand{ID: generateBatchID(), RequesterID: actorID, IdempotencyKey: key, RequestHash: fingerprint, ProvenanceVersion: 1, Retain: req.Retain}
		dispatches = nil
		created = false
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "requester_id"}, {Name: "idempotency_key"}}, DoNothing: true}).Create(&batch)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Clear the uninserted candidate ID: GORM otherwise adds it to the lookup.
			batch = model.BatchCommand{}
			if err := tx.Where("requester_id = ? AND idempotency_key = ?", actorID, key).First(&batch).Error; err != nil {
				return err
			}
			if batch.RequestHash != fingerprint {
				return errBatchIdempotencyConflict
			}
			if batch.DeletedAt != nil {
				return errBatchDeleted
			}
			return tx.Where("batch_id = ?", batch.ID).Order("task_id").Find(&dispatches).Error
		}
		var err error
		dispatches, err = insertBatchCommandTasksTx(tx, batch, req, nodes, nil)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	return batch, dispatches, created, err
}

// Each initial dispatch is claimed once before invoking the task manager. A
// request replay never repeats accepted, failed, or uncertain dispatches.
// Retry eligibility is determined by the batch retry path after refreshing its
// status, not by this per-task trigger receipt.
func (h *BatchHandler) dispatchBatch(ctx context.Context, batchID string, dispatches []model.BatchCommandDispatch) error {
	for i := range dispatches {
		dispatch := &dispatches[i]
		if dispatch.Status != "pending" {
			continue
		}
		claimed := false
		err := dbtx.WithSQLiteBusyRetryTx(ctx, h.db, func(tx *gorm.DB) error {
			claimed = false
			var batch model.BatchCommand
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ?", batchID).Error; err != nil {
				return err
			}
			if batch.DeletedAt != nil {
				return errBatchDeleted
			}
			result := tx.Model(&model.BatchCommandDispatch{}).Where("task_id = ? AND status = ?", dispatch.TaskID, "pending").Update("status", "dispatching")
			if result.Error != nil {
				return result.Error
			}
			claimed = result.RowsAffected == 1
			return nil
		})
		if err != nil {
			return err
		}
		if !claimed {
			if err := h.db.WithContext(ctx).First(dispatch, "task_id = ?", dispatch.TaskID).Error; err != nil {
				return err
			}
			continue
		}
		dispatch.Status = "dispatching"
		var runID uint
		var triggerErr error
		if h.manager == nil {
			triggerErr = errors.New("task manager unavailable")
		} else {
			runID, triggerErr = h.manager.TriggerManual(dispatch.TaskID)
		}
		status, message := "accepted", ""
		if triggerErr != nil || runID == 0 {
			status, message = "failed", "任务触发失败；请刷新批次并检查显式重试资格"
		}
		// A failed receipt write leaves dispatching (unknown), never a forged
		// success or an automatic second execution on request replay.
		result := h.db.WithContext(ctx).Model(&model.BatchCommandDispatch{}).Where("task_id = ? AND status = ?", dispatch.TaskID, "dispatching").Updates(map[string]any{"status": status, "run_id": runID, "last_error": message})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("batch dispatch receipt changed")
		}
		dispatch.Status, dispatch.RunID, dispatch.LastError = status, runID, message
	}
	return nil
}
