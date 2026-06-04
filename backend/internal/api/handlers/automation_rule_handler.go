package handlers

import (
	"errors"
	"strings"

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
