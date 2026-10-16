package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/dbtx"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errBatchRetrySourceUnavailable        = errors.New("batch retry source unavailable")
	errBatchRetryIneligible               = errors.New("batch retry source ineligible")
	errBatchRetryDuplicateNode            = errors.New("batch retry selection contains duplicate node")
	errBatchRetryOwnershipDenied          = errors.New("batch retry ownership denied")
	errBatchRetryActorDenied              = errors.New("batch retry actor denied")
	errBatchRetryStepUpDenied             = errors.New("batch retry step-up denied")
	errBatchRetryAuthorizationUnavailable = errors.New("batch retry authorization unavailable")
)

type batchRetrySourceRequest struct {
	TaskID *uint `json:"task_id"`
	RunID  *uint `json:"run_id"`
}

type batchRetryRequest struct {
	Sources *[]batchRetrySourceRequest `json:"sources"`
}

type batchRetryRequestHash struct {
	Kind          string             `json:"kind"`
	SourceBatchID string             `json:"source_batch_id"`
	Sources       []batchRetrySource `json:"sources"`
}

type batchRetryEligibilityDTO struct {
	TaskID       uint    `json:"task_id"`
	NodeID       uint    `json:"node_id"`
	RunID        uint    `json:"run_id"`
	Eligible     bool    `json:"eligible"`
	Reason       string  `json:"reason"`
	FailureKind  *string `json:"failure_kind"`
	RetryBatchID *string `json:"retry_batch_id"`
}

type batchRetryLinkDTO struct {
	TaskID  uint   `json:"task_id"`
	RunID   uint   `json:"run_id"`
	BatchID string `json:"batch_id"`
	Deleted bool   `json:"deleted"`
}

type batchRetryEligibility struct {
	batchRetryEligibilityDTO
	command string
}

type batchRetrySourceMetadata struct {
	TaskID         uint
	TaskNodeID     uint
	DispatchNodeID uint
}

type batchRetryGrantDenied struct {
	match credentialGrantMatch
	err   error
}

func (e *batchRetryGrantDenied) Error() string {
	if e == nil || e.err == nil {
		return "batch retry credential grant denied"
	}
	return e.err.Error()
}

func (e *batchRetryGrantDenied) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func decodeBatchRetryRequest(c *gin.Context) ([]batchRetrySource, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, errors.New("retry request body is missing")
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(payload) == 0 || len(payload) > 1<<20 || rejectDuplicateBackupContentJSON(payload) != nil {
		return nil, errors.New("retry request body is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request batchRetryRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("retry request contains trailing data")
	}
	if request.Sources == nil || len(*request.Sources) == 0 {
		return nil, errors.New("retry sources must not be empty")
	}
	sources := make([]batchRetrySource, 0, len(*request.Sources))
	seenTasks := make(map[uint]struct{}, len(*request.Sources))
	for _, source := range *request.Sources {
		if source.TaskID == nil || *source.TaskID == 0 || source.RunID == nil {
			return nil, errors.New("retry source task_id and run_id are required")
		}
		if _, exists := seenTasks[*source.TaskID]; exists {
			return nil, errors.New("retry source task_id must be unique")
		}
		seenTasks[*source.TaskID] = struct{}{}
		sources = append(sources, batchRetrySource{TaskID: *source.TaskID, RunID: *source.RunID})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].TaskID < sources[j].TaskID })
	return sources, nil
}

func batchRetryRequestFingerprint(sourceBatchID string, sources []batchRetrySource) (string, error) {
	payload, err := json.Marshal(batchRetryRequestHash{
		Kind:          "batch_command.retry.v1",
		SourceBatchID: sourceBatchID,
		Sources:       sources,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// Retry godoc
// @Summary      显式重试批量命令
// @Description  为已确认失败且来源可验证的批量命令创建全新子批次
// @Tags         batch
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "请求者范围的幂等键；同一请求重试必须复用"
// @Param        batch_id path string true "来源批次 ID"
// @Param        body body batchRetryRequest true "仅包含来源 task/run 对"
// @Success      200 {object} handlers.Response
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Router       /batch-commands/{batch_id}/retry [post]
func (h *BatchHandler) Retry(c *gin.Context) {
	if h == nil || h.db == nil {
		respondInternalError(c, errors.New("batch retry handler unavailable"))
		return
	}
	sourceBatchID := strings.TrimSpace(c.Param("batch_id"))
	if sourceBatchID == "" {
		respondBadRequest(c, "batch_id 不能为空")
		return
	}
	sources, err := decodeBatchRetryRequest(c)
	if err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, validKey := exactIdempotencyKey(c.Request)
	if !validKey {
		respondBadRequest(c, "需要有效的 Idempotency-Key")
		return
	}
	requestHash, err := batchRetryRequestFingerprint(sourceBatchID, sources)
	if err != nil {
		respondInternalError(c, err)
		return
	}

	actorID := middleware.CurrentUserID(c)
	if actorID == 0 {
		respondForbidden(c, "认证状态已变化，请重新登录")
		return
	}

	// Idempotency is checked before reading the source batch. A committed replay
	// remains addressable even after the source was cleaned up.
	existing, found, err := h.lookupBatchRetryByKey(c, actorID, key)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if found {
		if existing.RequestHash != requestHash || existing.SourceBatchID == nil || *existing.SourceBatchID != sourceBatchID {
			respondConflict(c, "幂等键已用于不同请求")
			return
		}
		if existing.DeletedAt != nil {
			respondConflict(c, "幂等键对应的重试批次已删除")
			return
		}
		var existingDispatches []model.BatchCommandDispatch
		if err := h.db.WithContext(c.Request.Context()).Where("batch_id = ?", existing.ID).Order("task_id").Find(&existingDispatches).Error; err != nil {
			respondInternalError(c, err)
			return
		}
		if len(existingDispatches) == 0 {
			respondConflict(c, "幂等批次回执不可用")
			return
		}
		nodeIDs := batchRetryDispatchNodeIDs(existingDispatches)
		if !h.authorizeBatchRetryPreflight(c, nodeIDs) {
			return
		}
	} else {
		_, _, nodeIDs, err := h.loadBatchRetrySourceMetadata(c, sourceBatchID, sources)
		if err != nil {
			h.respondBatchRetryError(c, err)
			return
		}
		if !h.authorizeBatchRetryPreflight(c, nodeIDs) {
			return
		}
	}

	batch, dispatches, created, err := h.createBatchRetry(c, sourceBatchID, key, sources)
	if err != nil {
		h.respondBatchRetryError(c, err)
		return
	}
	dispatchErr := h.dispatchBatch(c.Request.Context(), batch.ID, dispatches)
	taskIDs := make([]uint, 0, len(dispatches))
	runIDs := make([]uint, 0, len(dispatches))
	for _, dispatch := range dispatches {
		taskIDs = append(taskIDs, dispatch.TaskID)
		runIDs = append(runIDs, dispatch.RunID)
	}
	_ = created // Creation is reflected by the returned identity; replay is identical.
	respondOK(c, gin.H{
		"batch_id":            batch.ID,
		"source_batch_id":     sourceBatchID,
		"task_ids":            taskIDs,
		"run_ids":             runIDs,
		"retain":              batch.Retain,
		"dispatches":          dispatches,
		"dispatch_incomplete": dispatchErr != nil,
	})
}

func (h *BatchHandler) lookupBatchRetryByKey(c *gin.Context, actorID uint, key string) (model.BatchCommand, bool, error) {
	var batch model.BatchCommand
	result := h.db.WithContext(c.Request.Context()).Where("requester_id = ? AND idempotency_key = ?", actorID, key).Limit(1).Find(&batch)
	if result.Error != nil {
		return model.BatchCommand{}, false, result.Error
	}
	return batch, result.RowsAffected == 1, nil
}

func (h *BatchHandler) authorizeBatchRetryPreflight(c *gin.Context, nodeIDs []uint) bool {
	if len(nodeIDs) == 0 {
		respondForbidden(c, "无权访问该节点")
		return false
	}
	allowed, err := authorizeNodeOwnershipSet(c, h.db, nodeIDs)
	if err != nil {
		respondInternalError(c, err)
		return false
	}
	for _, nodeID := range nodeIDs {
		if _, ok := allowed[nodeID]; !ok {
			respondForbidden(c, "无权访问该节点")
			return false
		}
	}
	if !EnforceStepUp(c, h.db, h.jwtManager, auth.StepUpActionBatchCommandRetry, sshutil.PurposeBatchCommand, "batch_retry") {
		return false
	}
	return EnforceBatchCommandRetryCredentialGrants(c, h.db, nodeIDs)
}

func (h *BatchHandler) respondBatchRetryError(c *gin.Context, err error) {
	var denied *batchRetryGrantDenied
	switch {
	case errors.Is(err, errBatchRetrySourceUnavailable):
		respondNotFound(c, "来源批次或来源任务不存在")
	case errors.Is(err, errBatchRetryDuplicateNode):
		respondBadRequest(c, "选择的任务映射到同一节点，不能一起重试")
	case errors.Is(err, errBatchRetryIneligible), errors.Is(err, errBatchRetryAlreadyClaimed), errors.Is(err, errBatchIdempotencyConflict), errors.Is(err, errBatchDeleted):
		respondConflict(c, "来源失败不可验证、已变化、已重试或幂等状态冲突")
	case errors.Is(err, errBatchRetryOwnershipDenied), errors.Is(err, errBatchRetryActorDenied):
		respondForbidden(c, "无权执行批量命令重试")
	case errors.Is(err, errBatchRetryStepUpDenied):
		writeStepUpCredentialAudit(c, h.db, nil, stepUpAuditOperation{ExpectedAction: auth.StepUpActionBatchCommandRetry, Action: string(auth.StepUpActionBatchCommandRetry), Purpose: sshutil.PurposeBatchCommand, Operation: "batch_retry"}, credentialaudit.OutcomeBlocked, "failed")
		respondStepUpRequired(c, auth.StepUpActionBatchCommandRetry)
	case errors.As(err, &denied):
		writeCredentialGrantBlockedAudit(c, h.db, claimsFromGinContext(c), denied.match, credentialGrantStatusForError(denied.err))
		respondCredentialGrantRequired(c, "required")
	case errors.Is(err, errBatchRetryAuthorizationUnavailable):
		respondInternalError(c, err)
	default:
		respondInternalError(c, err)
	}
}

func (h *BatchHandler) loadBatchRetrySourceMetadata(c *gin.Context, sourceBatchID string, sources []batchRetrySource) (model.BatchCommand, []batchRetrySourceMetadata, []uint, error) {
	var batch model.BatchCommand
	result := h.db.WithContext(c.Request.Context()).Where("id = ?", sourceBatchID).Limit(1).Find(&batch)
	if result.Error != nil {
		return model.BatchCommand{}, nil, nil, result.Error
	}
	if result.RowsAffected != 1 || batch.DeletedAt != nil {
		return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
	}
	if batch.ProvenanceVersion != 1 {
		return model.BatchCommand{}, nil, nil, errBatchRetryIneligible
	}

	taskIDs := make([]uint, 0, len(sources))
	for _, source := range sources {
		taskIDs = append(taskIDs, source.TaskID)
	}
	var tasks []model.Task
	if err := h.db.WithContext(c.Request.Context()).Select("id", "node_id", "batch_id").Where("id IN ? AND batch_id = ?", taskIDs, sourceBatchID).Order("id").Find(&tasks).Error; err != nil {
		return model.BatchCommand{}, nil, nil, err
	}
	if len(tasks) != len(sources) {
		return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
	}
	var dispatches []model.BatchCommandDispatch
	if err := h.db.WithContext(c.Request.Context()).Select("task_id", "batch_id", "node_id", "status", "run_id").Where("batch_id = ? AND task_id IN ?", sourceBatchID, taskIDs).Order("task_id").Find(&dispatches).Error; err != nil {
		return model.BatchCommand{}, nil, nil, err
	}
	if len(dispatches) != len(sources) {
		return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
	}
	taskByID := make(map[uint]model.Task, len(tasks))
	for _, task := range tasks {
		taskByID[task.ID] = task
	}
	dispatchByTaskID := make(map[uint]model.BatchCommandDispatch, len(dispatches))
	for _, dispatch := range dispatches {
		dispatchByTaskID[dispatch.TaskID] = dispatch
	}
	metadata := make([]batchRetrySourceMetadata, 0, len(sources))
	unionNodeIDs := make([]uint, 0, len(sources)*2)
	seenCurrentNodes := make(map[uint]struct{}, len(sources))
	seenDispatchNodes := make(map[uint]struct{}, len(sources))
	for _, source := range sources {
		task, taskOK := taskByID[source.TaskID]
		dispatch, dispatchOK := dispatchByTaskID[source.TaskID]
		if !taskOK || !dispatchOK || dispatch.BatchID != sourceBatchID || dispatch.TaskID != task.ID {
			return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
		}
		if task.NodeID == 0 || dispatch.NodeID == 0 {
			return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
		}
		if _, exists := seenCurrentNodes[task.NodeID]; exists {
			return model.BatchCommand{}, nil, nil, errBatchRetryDuplicateNode
		}
		if _, exists := seenDispatchNodes[dispatch.NodeID]; exists {
			return model.BatchCommand{}, nil, nil, errBatchRetryDuplicateNode
		}
		seenCurrentNodes[task.NodeID] = struct{}{}
		seenDispatchNodes[dispatch.NodeID] = struct{}{}
		unionNodeIDs = append(unionNodeIDs, task.NodeID, dispatch.NodeID)
		metadata = append(metadata, batchRetrySourceMetadata{TaskID: source.TaskID, TaskNodeID: task.NodeID, DispatchNodeID: dispatch.NodeID})
	}
	unionNodeIDs = uniqueSortedUint(unionNodeIDs)
	var nodes []model.Node
	if err := h.db.WithContext(c.Request.Context()).Select("id", "name", "archived").Where("id IN ?", unionNodeIDs).Find(&nodes).Error; err != nil {
		return model.BatchCommand{}, nil, nil, err
	}
	if len(nodes) != len(unionNodeIDs) {
		return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
	}
	for _, node := range nodes {
		if node.Archived {
			return model.BatchCommand{}, nil, nil, errBatchRetrySourceUnavailable
		}
	}
	return batch, metadata, unionNodeIDs, nil
}

func (h *BatchHandler) createBatchRetry(c *gin.Context, sourceBatchID, key string, sources []batchRetrySource) (model.BatchCommand, []model.BatchCommandDispatch, bool, error) {
	requestHash, err := batchRetryRequestFingerprint(sourceBatchID, sources)
	if err != nil {
		return model.BatchCommand{}, nil, false, err
	}
	actorID := middleware.CurrentUserID(c)
	var batch model.BatchCommand
	var dispatches []model.BatchCommandDispatch
	created := false
	err = dbtx.WithSQLiteBusyRetryTx(c.Request.Context(), h.db, func(tx *gorm.DB) error {
		// Replay lookup deliberately precedes source locking: source cleanup must
		// not turn a committed idempotent request into a new operation.
		var existing model.BatchCommand
		lookup := tx.Where("requester_id = ? AND idempotency_key = ?", actorID, key).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			if existing.RequestHash != requestHash || existing.ProvenanceVersion != 1 || existing.SourceBatchID == nil || *existing.SourceBatchID != sourceBatchID {
				return errBatchIdempotencyConflict
			}
			if existing.DeletedAt != nil {
				return errBatchDeleted
			}
			var existingDispatches []model.BatchCommandDispatch
			if err := tx.Where("batch_id = ?", existing.ID).Order("task_id").Find(&existingDispatches).Error; err != nil {
				return err
			}
			if len(existingDispatches) == 0 {
				return errBatchRetryIneligible
			}
			if err := h.validateBatchRetryAuthorizationTx(c, tx, batchRetryDispatchNodeIDs(existingDispatches)); err != nil {
				return err
			}
			batch, dispatches, created = existing, existingDispatches, false
			return nil
		}

		var sourceBatch model.BatchCommand
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", sourceBatchID).Limit(1).Find(&sourceBatch)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || sourceBatch.DeletedAt != nil {
			return errBatchRetrySourceUnavailable
		}
		if sourceBatch.ProvenanceVersion != 1 {
			return errBatchRetryIneligible
		}

		taskIDs := make([]uint, 0, len(sources))
		for _, source := range sources {
			taskIDs = append(taskIDs, source.TaskID)
		}
		var lockedTasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "node_id", "batch_id", "status", "enabled", "archived_at", "retry_count", "last_run_at", "source", "executor_type", "executor_config", "policy_id", "depends_on_task_id", "cron_spec", "rsync_source", "rsync_target").Where("id IN ? AND batch_id = ?", taskIDs, sourceBatchID).Order("id").Find(&lockedTasks).Error; err != nil {
			return err
		}
		if len(lockedTasks) != len(sources) {
			return errBatchRetrySourceUnavailable
		}
		var lockedDispatches []model.BatchCommandDispatch
		if err := tx.Where("batch_id = ? AND task_id IN ?", sourceBatchID, taskIDs).Order("task_id").Find(&lockedDispatches).Error; err != nil {
			return err
		}
		if len(lockedDispatches) != len(sources) {
			return errBatchRetrySourceUnavailable
		}
		taskByID := make(map[uint]model.Task, len(lockedTasks))
		for _, task := range lockedTasks {
			taskByID[task.ID] = task
		}
		dispatchByTaskID := make(map[uint]model.BatchCommandDispatch, len(lockedDispatches))
		for _, dispatch := range lockedDispatches {
			dispatchByTaskID[dispatch.TaskID] = dispatch
		}
		unionNodeIDs := make([]uint, 0, len(sources)*2)
		for _, source := range sources {
			task, ok := taskByID[source.TaskID]
			if !ok {
				return errBatchRetrySourceUnavailable
			}
			dispatch, ok := dispatchByTaskID[source.TaskID]
			if !ok || dispatch.TaskID != task.ID || dispatch.BatchID != sourceBatchID || task.NodeID == 0 || dispatch.NodeID == 0 {
				return errBatchRetrySourceUnavailable
			}
			unionNodeIDs = append(unionNodeIDs, task.NodeID, dispatch.NodeID)
		}
		unionNodeIDs = uniqueSortedUint(unionNodeIDs)
		if err := h.validateBatchRetryAuthorizationTx(c, tx, unionNodeIDs); err != nil {
			return err
		}

		// Re-read the requester/key after the Batch -> Task locks. A request
		// that committed while this transaction waited owns the same identity.
		lookup = tx.Where("requester_id = ? AND idempotency_key = ?", actorID, key).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 1 {
			if existing.RequestHash != requestHash || existing.ProvenanceVersion != 1 || existing.SourceBatchID == nil || *existing.SourceBatchID != sourceBatchID {
				return errBatchIdempotencyConflict
			}
			if existing.DeletedAt != nil {
				return errBatchDeleted
			}
			if err := tx.Where("batch_id = ?", existing.ID).Order("task_id").Find(&dispatches).Error; err != nil {
				return err
			}
			if len(dispatches) == 0 {
				return errBatchRetryIneligible
			}
			if err := h.validateBatchRetryAuthorizationTx(c, tx, batchRetryDispatchNodeIDs(dispatches)); err != nil {
				return err
			}
			batch, created = existing, false
			return nil
		}

		// Commands are read only after the authorization checks. Re-fetch the
		// locked rows with their persisted command bytes for source validation.
		var tasks []model.Task
		if err := tx.Where("id IN ? AND batch_id = ?", taskIDs, sourceBatchID).Order("id").Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != len(sources) {
			return errBatchRetrySourceUnavailable
		}
		taskByID = make(map[uint]model.Task, len(tasks))
		for _, task := range tasks {
			taskByID[task.ID] = task
		}
		var nodes []model.Node
		if err := tx.Select("id", "name", "archived").Where("id IN ?", unionNodeIDs).Find(&nodes).Error; err != nil {
			return err
		}
		if len(nodes) != len(unionNodeIDs) {
			return errBatchRetrySourceUnavailable
		}
		nodeByID := make(map[uint]model.Node, len(nodes))
		for _, node := range nodes {
			if node.Archived {
				return errBatchRetryIneligible
			}
			nodeByID[node.ID] = node
		}

		command := ""
		batchNodes := make([]batchNode, 0, len(sources))
		for _, source := range sources {
			task, ok := taskByID[source.TaskID]
			if !ok {
				return errBatchRetrySourceUnavailable
			}
			dispatch := dispatchByTaskID[source.TaskID]
			requestedRunID := source.RunID
			candidate, err := assessBatchRetryEligibility(tx, sourceBatch, task, dispatch, &requestedRunID)
			if err != nil {
				return err
			}
			if !candidate.Eligible {
				if candidate.Reason == batchRetryReasonAlreadyRetried {
					return errBatchRetryAlreadyClaimed
				}
				return errBatchRetryIneligible
			}
			if command == "" {
				command = task.Command
			} else if command != task.Command {
				return errBatchRetryIneligible
			}
			batchNodes = append(batchNodes, batchNode{ID: task.NodeID, Name: nodeByID[task.NodeID].Name})
		}
		if command == "" || len(batchNodes) != len(sources) {
			return errBatchRetryIneligible
		}
		childSourceID := sourceBatch.ID
		batch = model.BatchCommand{
			ID:                generateBatchID(),
			RequesterID:       actorID,
			IdempotencyKey:    key,
			RequestHash:       requestHash,
			ProvenanceVersion: 1,
			SourceBatchID:     &childSourceID,
			Retain:            sourceBatch.Retain,
		}
		insertResult := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "requester_id"},
				{Name: "idempotency_key"},
			},
			DoNothing: true,
		}).Create(&batch)
		if insertResult.Error != nil {
			return insertResult.Error
		}
		if insertResult.RowsAffected == 0 {
			// A concurrent Create or Retry may have won the requester/key
			// identity. Adjudicate the durable winner rather than returning
			// driver-specific unique-constraint text.
			var winner model.BatchCommand
			lookup := tx.Where("requester_id = ? AND idempotency_key = ?", actorID, key).Limit(1).Find(&winner)
			if lookup.Error != nil {
				return lookup.Error
			}
			if lookup.RowsAffected != 1 || winner.RequestHash != requestHash || winner.ProvenanceVersion != 1 || winner.SourceBatchID == nil || *winner.SourceBatchID != sourceBatchID {
				return errBatchIdempotencyConflict
			}
			if winner.DeletedAt != nil {
				return errBatchDeleted
			}
			var winnerDispatches []model.BatchCommandDispatch
			if err := tx.Where("batch_id = ?", winner.ID).Order("task_id").Find(&winnerDispatches).Error; err != nil {
				return err
			}
			if len(winnerDispatches) == 0 {
				return errBatchRetryIneligible
			}
			if err := h.validateBatchRetryAuthorizationTx(c, tx, batchRetryDispatchNodeIDs(winnerDispatches)); err != nil {
				return err
			}
			batch, dispatches, created = winner, winnerDispatches, false
			return nil
		}
		childRequest := batchCommandRequest{Command: command, Name: "重试批次 " + sourceBatch.ID, Retain: sourceBatch.Retain}
		var err error
		dispatches, err = insertBatchCommandTasksTx(tx, batch, childRequest, batchNodes, sources)
		if err != nil {
			return err
		}
		audit := credentialaudit.FromGin(c, credentialaudit.Event{
			Action:   string(auth.StepUpActionBatchCommandRetry),
			Purpose:  sshutil.PurposeBatchCommand,
			Outcome:  credentialaudit.OutcomeSuccess,
			Metadata: map[string]any{"stage": "created", "source_batch_id": sourceBatch.ID, "batch_id": batch.ID, "node_count": len(batchNodes), "task_count": len(batchNodes), "retain": sourceBatch.Retain},
		})
		if err := credentialaudit.Write(tx, audit); err != nil {
			return err
		}
		created = true
		return nil
	})
	return batch, dispatches, created, err
}

func (h *BatchHandler) validateBatchRetryAuthorizationTx(c *gin.Context, tx *gorm.DB, nodeIDs []uint) error {
	nodeIDs = uniqueSortedUint(nodeIDs)
	if len(nodeIDs) == 0 {
		return errBatchRetryOwnershipDenied
	}
	allowed, err := authorizeNodeOwnershipSet(c, tx, nodeIDs)
	if err != nil {
		return fmt.Errorf("%w: ownership lookup: %v", errBatchRetryAuthorizationUnavailable, err)
	}
	for _, nodeID := range nodeIDs {
		if _, ok := allowed[nodeID]; !ok {
			return errBatchRetryOwnershipDenied
		}
	}
	// Grants belong to the primary session, not the separate purpose-bound
	// step-up proof. Preserve the middleware's authenticated version snapshot.
	claims := claimsFromGinContext(c)
	if claims == nil || (claims.Role != "admin" && claims.Role != "operator") {
		return errBatchRetryActorDenied
	}
	actorID, role := claims.UserID, claims.Role
	if actorID == 0 {
		return errBatchRetryActorDenied
	}
	var user model.User
	result := tx.Select("id", "role", "token_version", "totp_reenrollment_required").Where("id = ?", actorID).Limit(1).Find(&user)
	if result.Error != nil {
		return fmt.Errorf("%w: actor lookup: %v", errBatchRetryAuthorizationUnavailable, result.Error)
	}
	if result.RowsAffected != 1 || user.Role != role || user.TokenVersion != claims.TokenVersion || user.TOTPReenrollmentRequired {
		return errBatchRetryActorDenied
	}
	proof := strings.TrimSpace(c.GetHeader(StepUpHeaderName))
	_, err = validateStepUpProof(tx, h.jwtManager, proof, actorID, role, auth.StepUpActionBatchCommandRetry)
	if err != nil {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			return fmt.Errorf("%w: %v", errBatchRetryAuthorizationUnavailable, err)
		}
		return fmt.Errorf("%w: %v", errBatchRetryStepUpDenied, err)
	}
	for _, nodeID := range nodeIDs {
		match := credentialGrantMatch{Action: CredentialGrantActionBatchCommandRetry, Purpose: sshutil.PurposeBatchCommand, NodeID: credentialaudit.PtrUint(nodeID)}
		if _, err := findActiveCredentialGrant(c.Request.Context(), tx, claims, match); err != nil {
			if isCredentialGrantDenialError(err) {
				return &batchRetryGrantDenied{match: match, err: err}
			}
			return fmt.Errorf("%w: grant lookup: %v", errBatchRetryAuthorizationUnavailable, err)
		}
	}
	return nil
}

func assessBatchRetryEligibility(db *gorm.DB, batch model.BatchCommand, task model.Task, dispatch model.BatchCommandDispatch, requestedRunID *uint) (batchRetryEligibility, error) {
	result := batchRetryEligibility{batchRetryEligibilityDTO: batchRetryEligibilityDTO{TaskID: task.ID, NodeID: task.NodeID, RunID: 0, Eligible: false, Reason: batchRetryReasonNotFailed}}
	if batch.ProvenanceVersion != 1 || batch.DeletedAt != nil {
		result.Reason = batchRetryReasonLegacy
		return result, nil
	}

	var latestRun model.TaskRun
	latestRunFound := false
	latestRunSucceeded := false
	if dispatch.Status == "accepted" && dispatch.RunID > 0 {
		runQuery := db.Select("id", "status", "finished_at", "node_id_snapshot", "executor_type_snapshot").Where("task_id = ?", task.ID).Order("id DESC").Limit(1).Find(&latestRun)
		if runQuery.Error != nil {
			return result, runQuery.Error
		}
		if runQuery.RowsAffected == 1 {
			latestRunFound = true
			result.RunID = latestRun.ID
			latestRunSucceeded = latestRun.Status == model.TaskRunStatusSuccess
			if !latestRunSucceeded {
				failureKind := batchRetryFailureKindExecution
				result.FailureKind = &failureKind
			}
			// A POST must identify an actually retained latest run. Zero is
			// reserved for the dispatch-failure branch; GET passes nil and
			// discovers the current run here.
			if requestedRunID != nil && (*requestedRunID == 0 || *requestedRunID != latestRun.ID) {
				result.Reason = batchRetryReasonNotFailed
				return result, nil
			}
		}
	}
	if task.ID == 0 || task.BatchID != batch.ID || dispatch.TaskID != task.ID || dispatch.BatchID != batch.ID || task.NodeID == 0 || dispatch.NodeID == 0 || task.NodeID != dispatch.NodeID {
		result.Reason = batchRetryReasonSourceChanged
		return result, nil
	}
	fingerprint := model.BatchCommandTaskFingerprint(task)
	result.command = task.Command
	if fingerprint == "" || dispatch.CommandFingerprint == "" || fingerprint != dispatch.CommandFingerprint || len(strings.TrimSpace(task.Command)) > maxCommandLength || isDangerousCommand(strings.TrimSpace(task.Command)) || !task.Enabled || task.ArchivedAt != nil {
		result.Reason = batchRetryReasonSourceChanged
		return result, nil
	}
	var node model.Node
	nodeResult := db.Select("id", "archived").Where("id = ?", dispatch.NodeID).Limit(1).Find(&node)
	if nodeResult.Error != nil {
		return result, nodeResult.Error
	}
	if nodeResult.RowsAffected != 1 || node.Archived {
		result.Reason = batchRetryReasonSourceMissing
		return result, nil
	}
	active, err := batchRetryTaskActive(db, task.ID, task.Status)
	if err != nil {
		return result, err
	}
	if active {
		result.Reason = batchRetryReasonActive
		return result, nil
	}
	effectsPending, err := batchRetryTaskEffectsPending(db, task.ID)
	if err != nil {
		return result, err
	}
	if effectsPending {
		result.Reason = batchRetryReasonEffectsPending
		return result, nil
	}

	if dispatch.Status == "failed" && dispatch.RunID == 0 {
		failureKind := batchRetryFailureKindDispatch
		result.FailureKind = &failureKind
		if requestedRunID != nil && *requestedRunID != 0 {
			result.Reason = batchRetryReasonNotFailed
			return result, nil
		}
		if task.Status != model.TaskRunStatusPending || task.LastRunAt != nil || task.RetryCount != 0 {
			result.Reason = batchRetryReasonNotFailed
			return result, nil
		}
		var runCount, evidenceCount int64
		if err := db.Model(&model.TaskRun{}).Where("task_id = ?", task.ID).Count(&runCount).Error; err != nil {
			return result, err
		}
		if err := db.Model(&model.BatchCommandRunEvidence{}).Where("task_id = ?", task.ID).Count(&evidenceCount).Error; err != nil {
			return result, err
		}
		if runCount != 0 || evidenceCount != 0 {
			result.Reason = batchRetryReasonHistoryUnverified
			return result, nil
		}
		result.Eligible = true
		result.Reason = batchRetryReasonEligible
		if err := batchRetryApplyClaim(db, &result, task.ID, 0); err != nil {
			return result, err
		}
		return result, nil
	}
	if dispatch.Status != "accepted" || dispatch.RunID == 0 {
		if task.Status == model.TaskRunStatusRunning || task.Status == model.TaskRunStatusRetrying {
			result.Reason = batchRetryReasonActive
		}
		return result, nil
	}
	if latestRunSucceeded {
		// A completed successful run is a normal terminal result, not an
		// unverified failure. Keep it non-retryable without an execution
		// failure label even if no retry claim exists.
		result.FailureKind = nil
		result.Reason = batchRetryReasonNotFailed
		return result, nil
	}
	failureKind := batchRetryFailureKindExecution
	result.FailureKind = &failureKind
	var runs []model.TaskRun
	if err := db.Where("task_id = ?", task.ID).Order("id DESC").Find(&runs).Error; err != nil {
		return result, err
	}
	var evidences []model.BatchCommandRunEvidence
	if err := db.Where("task_id = ?", task.ID).Order("run_id").Find(&evidences).Error; err != nil {
		return result, err
	}
	if len(runs) == 0 || len(evidences) == 0 || !latestRunFound {
		result.Reason = batchRetryReasonHistoryUnverified
		result.RunID = 0
		return result, nil
	}
	latest := runs[0]
	result.RunID = latest.ID
	byRun := make(map[uint]model.BatchCommandRunEvidence, len(evidences))
	maxEvidenceRunID := uint(0)
	for _, evidence := range evidences {
		byRun[evidence.RunID] = evidence
		if evidence.RunID > maxEvidenceRunID {
			maxEvidenceRunID = evidence.RunID
		}
		if evidence.TaskID != task.ID || evidence.NodeID != dispatch.NodeID || evidence.CommandFingerprint != fingerprint || evidence.FinishedAt == nil || (evidence.Outcome == "exit_failure" && (evidence.ExitCode == nil || *evidence.ExitCode <= 0)) || (evidence.Outcome == "not_started" && evidence.ExitCode != nil) || (evidence.Outcome != "exit_failure" && evidence.Outcome != "not_started") {
			result.Reason = batchRetryReasonHistoryUnverified
			return result, nil
		}
	}
	for _, run := range runs {
		if _, ok := byRun[run.ID]; !ok {
			result.Reason = batchRetryReasonHistoryUnverified
			return result, nil
		}
	}
	if maxEvidenceRunID != latest.ID {
		result.Reason = batchRetryReasonHistoryUnverified
		result.RunID = 0
		return result, nil
	}
	// All retained evidence is now checked below; any gap remains distinct
	// from an active task because activity was checked above.
	if task.Status != model.TaskRunStatusFailed || latest.Status != model.TaskRunStatusFailed || latest.FinishedAt == nil || latest.NodeIDSnapshot != dispatch.NodeID || !strings.EqualFold(strings.TrimSpace(latest.ExecutorTypeSnapshot), strings.TrimSpace(task.ExecutorType)) {
		result.Reason = batchRetryReasonNotFailed
		return result, nil
	}
	result.Eligible = true
	result.Reason = batchRetryReasonEligible
	if err := batchRetryApplyClaim(db, &result, task.ID, latest.ID); err != nil {
		return result, err
	}
	return result, nil
}

func batchRetryTaskActive(db *gorm.DB, taskID uint, taskStatus string) (bool, error) {
	if taskStatus == model.TaskRunStatusRunning || taskStatus == model.TaskRunStatusRetrying {
		return true, nil
	}
	var active int64
	if err := db.Model(&model.TaskRun{}).Where("task_id = ? AND status IN ?", taskID, model.TaskRunActiveStatuses()).Count(&active).Error; err != nil {
		return false, err
	}
	return active > 0, nil
}

func batchRetryTaskEffectsPending(db *gorm.DB, taskID uint) (bool, error) {
	var pending int64
	if err := db.Table("task_run_effects").Joins("JOIN task_runs ON task_runs.id = task_run_effects.task_run_id").Where("task_runs.task_id = ? AND task_run_effects.status <> ?", taskID, model.TaskRunEffectStatusSucceeded).Count(&pending).Error; err != nil {
		return false, err
	}
	return pending > 0, nil
}

func batchRetryApplyClaim(db *gorm.DB, result *batchRetryEligibility, taskID, runID uint) error {
	var claimed model.BatchCommandDispatch
	query := db.Select("batch_id").Where("retry_of_task_id = ? AND retry_of_run_id = ?", taskID, runID).Limit(1).Find(&claimed)
	if query.Error != nil {
		return query.Error
	}
	if query.RowsAffected == 1 {
		result.Eligible = false
		result.Reason = batchRetryReasonAlreadyRetried
		result.RetryBatchID = &claimed.BatchID
	}
	return nil
}

func (h *BatchHandler) buildBatchRetryMetadata(c *gin.Context, batch model.BatchCommand, tasks []model.Task, dispatches []model.BatchCommandDispatch) ([]batchRetryEligibilityDTO, []batchRetryLinkDTO, error) {
	if tasks == nil {
		tasks = []model.Task{}
	}
	dispatchByTask := make(map[uint]model.BatchCommandDispatch, len(dispatches))
	for _, dispatch := range dispatches {
		dispatchByTask[dispatch.TaskID] = dispatch
	}
	eligibility := make([]batchRetryEligibilityDTO, 0, len(tasks))
	for _, task := range tasks {
		dispatch, ok := dispatchByTask[task.ID]
		if !ok {
			eligibility = append(eligibility, batchRetryEligibilityDTO{TaskID: task.ID, NodeID: task.NodeID, RunID: 0, Eligible: false, Reason: batchRetryReasonSourceMissing, FailureKind: nil, RetryBatchID: nil})
			continue
		}
		candidate, err := assessBatchRetryEligibility(h.db, batch, task, dispatch, nil)
		if err != nil {
			return nil, nil, err
		}
		eligibility = append(eligibility, candidate.batchRetryEligibilityDTO)
	}
	links, err := h.batchRetryLinks(c, dispatches)
	if err != nil {
		return nil, nil, err
	}
	return eligibility, links, nil
}

func (h *BatchHandler) batchRetryLinks(c *gin.Context, dispatches []model.BatchCommandDispatch) ([]batchRetryLinkDTO, error) {
	links := make([]batchRetryLinkDTO, 0)
	if len(dispatches) == 0 {
		return links, nil
	}
	taskIDs := make([]uint, 0, len(dispatches))
	for _, dispatch := range dispatches {
		if dispatch.TaskID == 0 {
			continue
		}
		taskIDs = append(taskIDs, dispatch.TaskID)
	}
	if len(taskIDs) == 0 {
		return links, nil
	}
	var consumed []model.BatchCommandDispatch
	if err := h.db.WithContext(c.Request.Context()).Select("batch_id", "retry_of_task_id", "retry_of_run_id").Where("retry_of_task_id IN ?", uniqueSortedUint(taskIDs)).Find(&consumed).Error; err != nil {
		return nil, err
	}
	batchIDs := make([]string, 0, len(consumed))
	seenBatchIDs := make(map[string]struct{}, len(consumed))
	for _, dispatch := range consumed {
		if dispatch.RetryOfTaskID == nil || dispatch.RetryOfRunID == nil {
			continue
		}
		links = append(links, batchRetryLinkDTO{TaskID: *dispatch.RetryOfTaskID, RunID: *dispatch.RetryOfRunID, BatchID: dispatch.BatchID})
		if _, seen := seenBatchIDs[dispatch.BatchID]; !seen {
			seenBatchIDs[dispatch.BatchID] = struct{}{}
			batchIDs = append(batchIDs, dispatch.BatchID)
		}
	}
	if len(batchIDs) == 0 {
		return links, nil
	}
	var batches []model.BatchCommand
	if err := h.db.WithContext(c.Request.Context()).Select("id", "deleted_at").Where("id IN ?", batchIDs).Find(&batches).Error; err != nil {
		return nil, err
	}
	deleted := make(map[string]bool, len(batches))
	for _, batch := range batches {
		deleted[batch.ID] = batch.DeletedAt != nil
	}
	for i := range links {
		links[i].Deleted = deleted[links[i].BatchID]
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].TaskID != links[j].TaskID {
			return links[i].TaskID < links[j].TaskID
		}
		if links[i].RunID != links[j].RunID {
			return links[i].RunID < links[j].RunID
		}
		return links[i].BatchID < links[j].BatchID
	})
	return links, nil
}

func batchRetryDispatchNodeIDs(dispatches []model.BatchCommandDispatch) []uint {
	nodeIDs := make([]uint, 0, len(dispatches))
	for _, dispatch := range dispatches {
		if dispatch.NodeID > 0 {
			nodeIDs = append(nodeIDs, dispatch.NodeID)
		}
	}
	return uniqueSortedUint(nodeIDs)
}

func uniqueSortedUint(values []uint) []uint {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]uint(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := sorted[:0]
	for _, value := range sorted {
		if value == 0 || (len(out) > 0 && out[len(out)-1] == value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

const (
	batchRetryReasonEligible          = "eligible"
	batchRetryReasonLegacy            = "legacy"
	batchRetryReasonSourceChanged     = "source_changed"
	batchRetryReasonSourceMissing     = "source_missing"
	batchRetryReasonNotFailed         = "not_failed"
	batchRetryReasonActive            = "active"
	batchRetryReasonEffectsPending    = "effects_pending"
	batchRetryReasonHistoryUnverified = "history_unverified"
	batchRetryReasonAlreadyRetried    = "already_retried"
	batchRetryFailureKindDispatch     = "dispatch"
	batchRetryFailureKindExecution    = "execution"
)
