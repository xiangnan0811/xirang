package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/taskstats"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TaskStatisticsHandler serves the task-only historical statistics endpoint.
type TaskStatisticsHandler struct {
	db *gorm.DB
}

func NewTaskStatisticsHandler(db *gorm.DB) *TaskStatisticsHandler {
	return &TaskStatisticsHandler{db: db}
}

// taskStatisticsPayload deliberately contains only the public task statistics
// contract. DisallowUnknownFields below makes node_ids and client-supplied
// ownership fields hard failures instead of silently ignored input.
type taskStatisticsPayload struct {
	Metric      string                `json:"metric"`
	Filters     taskStatisticsFilters `json:"filters"`
	Aggregation string                `json:"aggregation"`
	Start       time.Time             `json:"start"`
	End         time.Time             `json:"end"`
}

type taskStatisticsFilters struct {
	TaskIDs []uint `json:"task_ids,omitempty"`
}

// Query returns authorized historical task-run statistics.
// @Summary 查询历史任务统计
// @Tags tasks
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body taskStatisticsPayload true "任务指标、聚合方式与半开时间窗口"
// @Success 200 {object} handlers.Response{data=taskstats.QueryResponse}
// @Failure 400 {object} handlers.Response
// @Failure 401 {object} handlers.Response
// @Failure 403 {object} handlers.Response
// @Router /tasks/statistics/query [post]
func (h *TaskStatisticsHandler) Query(c *gin.Context) {
	payload, err := decodeTaskStatisticsPayload(c.Request.Body)
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	req := taskstats.QueryRequest{
		Metric:      payload.Metric,
		Filters:     taskstats.Filters{TaskIDs: payload.Filters.TaskIDs},
		Aggregation: payload.Aggregation,
		Start:       payload.Start.UTC(),
		End:         payload.End.UTC(),
	}

	ownedIDs, needFilter, err := ownershipNodeFilter(c, h.db)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	if needFilter {
		if len(req.Filters.TaskIDs) == 0 {
			// Empty task_ids means all tasks visible to the current operator;
			// scope at query time without materializing an IN list of task IDs.
			req.RestrictToNodeIDs(ownedIDs)
		} else {
			allowed, err := h.authorizeExplicitTasks(c, req.Filters.TaskIDs, ownedIDs)
			if err != nil {
				respondInternalError(c, err)
				return
			}
			if !allowed {
				respondForbidden(c, "无权查询未授权节点上的任务统计")
				return
			}
		}
	}

	response, err := taskstats.Query(c.Request.Context(), h.db, req)
	if err != nil {
		switch {
		case errors.Is(err, taskstats.ErrInvalidMetric),
			errors.Is(err, taskstats.ErrInvalidAggregation),
			errors.Is(err, taskstats.ErrInvalidTimeRange):
			respondBadRequest(c, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			c.AbortWithStatus(499)
		default:
			respondInternalError(c, err)
		}
		return
	}
	respondOK(c, response)
}

func decodeTaskStatisticsPayload(reader io.Reader) (taskStatisticsPayload, error) {
	var payload taskStatisticsPayload
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return taskStatisticsPayload{}, err
	}

	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return taskStatisticsPayload{}, errors.New("request body must contain one JSON object")
		}
		return taskStatisticsPayload{}, err
	}
	return payload, nil
}

// authorizeExplicitTasks checks every requested task against the operator's
// owned node set. Missing tasks are forbidden too, avoiding a partial result or
// a fallback to an unrestricted query.
func (h *TaskStatisticsHandler) authorizeExplicitTasks(c *gin.Context, taskIDs, ownedNodeIDs []uint) (bool, error) {
	var tasks []model.Task
	if err := h.db.WithContext(c.Request.Context()).Select("id, node_id").Where("id IN ?", taskIDs).Find(&tasks).Error; err != nil {
		return false, err
	}
	owned := make(map[uint]struct{}, len(ownedNodeIDs))
	for _, nodeID := range ownedNodeIDs {
		owned[nodeID] = struct{}{}
	}
	byID := make(map[uint]uint, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task.NodeID
	}
	for _, taskID := range taskIDs {
		nodeID, ok := byID[taskID]
		if !ok {
			return false, nil
		}
		if _, ok := owned[nodeID]; !ok {
			return false, nil
		}
	}
	return true, nil
}
