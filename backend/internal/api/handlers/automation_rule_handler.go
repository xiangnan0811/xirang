package handlers

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AutomationRuleHandler struct {
	db *gorm.DB
}

func NewAutomationRuleHandler(db *gorm.DB) *AutomationRuleHandler {
	return &AutomationRuleHandler{db: db}
}

type automationRuleRequest struct {
	Name         string `json:"name" binding:"required"`
	Description  string `json:"description"`
	EventType    string `json:"event_type" binding:"required"`
	EventFilter  string `json:"event_filter"`
	ActionType   string `json:"action_type" binding:"required"`
	ActionConfig string `json:"action_config"`
	Enabled      *bool  `json:"enabled"`
}

// List godoc
// @Summary      列出自动化规则
// @Description  返回所有自动化规则列表
// @Tags         automation-rules
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]model.AutomationRule}
// @Failure      401  {object}  handlers.Response
// @Router       /automation-rules [get]
func (h *AutomationRuleHandler) List(c *gin.Context) {
	var items []model.AutomationRule
	if err := h.db.Order("id asc").Find(&items).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, items)
}

// automationRuleLogResponse deliberately excludes free-form diagnostics and configuration.
type automationRuleLogResponse struct {
	ID              uint      `json:"id"`
	RuleID          uint      `json:"rule_id"`
	EventType       string    `json:"event_type"`
	ActionType      string    `json:"action_type"`
	Result          string    `json:"result"`
	CreatedAt       time.Time `json:"created_at"`
	ErrorCode       *string   `json:"error_code"`
	TargetTaskID    *int64    `json:"target_task_id"`
	TargetTaskRunID *int64    `json:"target_task_run_id"`
}

func automationLogTargetID(raw json.RawMessage) *int64 {
	if len(raw) == 0 || raw[0] < '1' || raw[0] > '9' {
		return nil
	}
	value, err := json.Number(raw).Int64()
	if err != nil || value <= 0 || value > 9007199254740991 {
		return nil
	}
	return &value
}

func mapAutomationRuleLog(item model.AutomationRuleLog) automationRuleLogResponse {
	out := automationRuleLogResponse{
		ID: item.ID, RuleID: item.RuleID, CreatedAt: item.CreatedAt,
		EventType: "unknown", ActionType: "unknown", Result: "unknown",
	}
	if automation.ValidEventTypes[item.EventType] {
		out.EventType = item.EventType
	}
	if automation.ValidActionTypes[item.ActionType] {
		out.ActionType = item.ActionType
	}
	switch item.Result {
	case automation.ResultSuccess:
		out.Result = automation.ResultSuccess
	case automation.ResultError:
		out.Result = automation.ResultError
		code := "ACTION_FAILED"
		out.ErrorCode = &code
	}
	if out.ActionType == automation.ActionTriggerTask {
		var targets struct {
			TaskID    json.RawMessage `json:"task_id"`
			TaskRunID json.RawMessage `json:"task_run_id"`
		}
		if err := json.Unmarshal([]byte(item.Details), &targets); err == nil {
			taskID, runID := automationLogTargetID(targets.TaskID), automationLogTargetID(targets.TaskRunID)
			if taskID != nil && runID != nil {
				out.TargetTaskID, out.TargetTaskRunID = taskID, runID
			}
		}
	}
	return out
}

// ListLogs godoc
// @Summary      查询安全的自动化执行历史
// @Description  仅管理员；不公开原始错误、消息、配置或 details。成功仅表示动作已记录，不代表任务最终成功或通知已投递。
// @Tags         automation-rules
// @Security     Bearer
// @Produce      json
// @Param        rule_id    query int    false "规则 ID（允许已删除规则）"
// @Param        result     query string false "动作结果" Enums(success,error)
// @Param        page       query int    false "页码" default(1)
// @Param        page_size  query int    false "每页数量，最大 500" default(30)
// @Param        sort_order query string false "按日志 ID 排序" Enums(asc,desc) default(desc)
// @Success      200 {object} handlers.PaginatedResponse{data=[]automationRuleLogResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Router       /automation-rule-logs [get]
func (h *AutomationRuleHandler) ListLogs(c *gin.Context) {
	query := h.db.WithContext(c.Request.Context()).Model(&model.AutomationRuleLog{})
	if raw := c.Query("rule_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 63)
		if err != nil || id == 0 {
			respondBadRequest(c, "rule_id 必须为正整数")
			return
		}
		query = query.Where("rule_id = ?", id)
	}
	if result := c.Query("result"); result != "" {
		if result != automation.ResultSuccess && result != automation.ResultError {
			respondBadRequest(c, "result 必须为 success 或 error")
			return
		}
		query = query.Where("result = ?", result)
	}
	pagination := parsePagination(c, 30, "id", map[string]bool{"id": true})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	var rows []model.AutomationRuleLog
	if err := applyPagination(query, pagination).Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	items := make([]automationRuleLogResponse, len(rows))
	for i := range rows {
		items[i] = mapAutomationRuleLog(rows[i])
	}
	respondPaginated(c, items, total, pagination.Page, pagination.PageSize)
}

// Get godoc
// @Summary      获取自动化规则详情
// @Description  返回单个自动化规则
// @Tags         automation-rules
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "自动化规则 ID"
// @Success      200  {object}  handlers.Response{data=model.AutomationRule}
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /automation-rules/{id} [get]
func (h *AutomationRuleHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var item model.AutomationRule
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "自动化规则不存在")
		return
	}
	respondOK(c, item)
}

// Create godoc
// @Summary      创建自动化规则
// @Description  创建新的自动化规则
// @Tags         automation-rules
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      automationRuleRequest  true  "创建自动化规则请求"
// @Success      201   {object}  handlers.Response{data=model.AutomationRule}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /automation-rules [post]
func (h *AutomationRuleHandler) Create(c *gin.Context) {
	var req automationRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.EventType = strings.TrimSpace(req.EventType)
	req.ActionType = strings.TrimSpace(req.ActionType)

	if req.Name == "" {
		respondBadRequest(c, "名称不能为空")
		return
	}
	if len(req.Name) > 128 {
		respondBadRequest(c, "名称过长（最多 128 字符）")
		return
	}
	if !automation.ValidEventTypes[req.EventType] {
		respondBadRequest(c, "不支持的事件类型: "+req.EventType)
		return
	}
	if !automation.ValidActionTypes[req.ActionType] {
		respondBadRequest(c, "不支持的动作类型: "+req.ActionType)
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	filter := strings.TrimSpace(req.EventFilter)
	if filter == "" {
		filter = "{}"
	}
	config := strings.TrimSpace(req.ActionConfig)
	if config == "" {
		config = "{}"
	}

	item := model.AutomationRule{
		Name:         req.Name,
		Description:  strings.TrimSpace(req.Description),
		EventType:    req.EventType,
		EventFilter:  filter,
		ActionType:   req.ActionType,
		ActionConfig: config,
		Enabled:      enabled,
	}
	if err := h.db.Create(&item).Error; err != nil {
		err = apperr.WrapDBError(err)
		if errors.Is(err, apperr.ErrDuplicate) {
			respondConflict(c, "规则名称已存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondCreated(c, item)
}

// Update godoc
// @Summary      更新自动化规则
// @Description  完整更新自动化规则配置
// @Tags         automation-rules
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int                     true  "自动化规则 ID"
// @Param        body  body      automationRuleRequest  true  "更新自动化规则请求"
// @Success      200   {object}  handlers.Response{data=model.AutomationRule}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /automation-rules/{id} [put]
func (h *AutomationRuleHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req automationRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.EventType = strings.TrimSpace(req.EventType)
	req.ActionType = strings.TrimSpace(req.ActionType)

	if req.Name == "" {
		respondBadRequest(c, "名称不能为空")
		return
	}
	if !automation.ValidEventTypes[req.EventType] {
		respondBadRequest(c, "不支持的事件类型: "+req.EventType)
		return
	}
	if !automation.ValidActionTypes[req.ActionType] {
		respondBadRequest(c, "不支持的动作类型: "+req.ActionType)
		return
	}

	var item model.AutomationRule
	if err := h.db.First(&item, id).Error; err != nil {
		respondNotFound(c, "自动化规则不存在")
		return
	}

	item.Name = req.Name
	item.Description = strings.TrimSpace(req.Description)
	item.EventType = req.EventType
	item.ActionType = req.ActionType
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}

	filter := strings.TrimSpace(req.EventFilter)
	if filter == "" {
		filter = "{}"
	}
	item.EventFilter = filter

	config := strings.TrimSpace(req.ActionConfig)
	if config == "" {
		config = "{}"
	}
	item.ActionConfig = config

	if err := h.db.Save(&item).Error; err != nil {
		err = apperr.WrapDBError(err)
		if errors.Is(err, apperr.ErrDuplicate) {
			respondConflict(c, "规则名称已存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, item)
}

// Delete godoc
// @Summary      删除自动化规则
// @Description  删除指定自动化规则
// @Tags         automation-rules
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "自动化规则 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /automation-rules/{id} [delete]
func (h *AutomationRuleHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.AutomationRule{}, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "deleted")
}
