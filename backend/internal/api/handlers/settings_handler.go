package handlers

import (
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SettingsHandler 系统设置接口
type SettingsHandler struct {
	db  *gorm.DB
	svc *settings.Service
}

// NewSettingsHandler 创建设置处理器
func NewSettingsHandler(db *gorm.DB, svc *settings.Service) *SettingsHandler {
	return &SettingsHandler{db: db, svc: svc}
}

// GetAll godoc
// @Summary      获取所有系统设置
// @Description  返回设置定义列表和当前有效值
// @Tags         settings
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings [get]
func (h *SettingsHandler) GetAll(c *gin.Context) {
	result, err := h.svc.GetAll()
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{
		"definitions": h.svc.Registry(),
		"values":      result,
	})
}

// BatchUpdate godoc
// @Summary      批量更新系统设置
// @Description  批量更新系统设置（原子操作：先校验全部，再统一写入）
// @Tags         settings
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "键值对 map"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings [put]
func (h *SettingsHandler) BatchUpdate(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	// 预检：校验全部 key/value，不写入
	for key, value := range req {
		if err := h.svc.Validate(key, value); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}

	userID := c.GetUint(middleware.CtxUserID)

	// 记录旧值用于审计
	oldValues := make(map[string]string, len(req))
	for key := range req {
		oldValues[key] = h.svc.GetEffective(key)
	}

	// 原子写入：在事务中更新全部设置
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		for key, value := range req {
			if err := h.svc.UpdateWithTx(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		respondInternalError(c, err)
		return
	}

	// 审计日志
	for key, value := range req {
		logger.Module("audit").Info().
			Str("action", "settings_update").
			Str("key", key).
			Str("old_value", oldValues[key]).
			Str("new_value", value).
			Str("source", "db").
			Uint("user_id", userID).
			Msg("系统设置变更")
	}

	respondMessage(c, "设置已更新")
}

// Delete godoc
// @Summary      重置系统设置
// @Description  删除指定 key 的 DB 覆盖值，恢复为环境变量或默认值
// @Tags         settings
// @Security     Bearer
// @Produce      json
// @Param        key  path      string  true  "设置 Key"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings/{key} [delete]
func (h *SettingsHandler) Delete(c *gin.Context) {
	key := c.Param("key")
	oldVal := h.svc.GetEffective(key)
	if err := h.svc.Delete(key); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	newVal := h.svc.GetEffective(key)
	userID := c.GetUint(middleware.CtxUserID)
	logger.Module("audit").Info().
		Str("action", "settings_reset").
		Str("key", key).
		Str("old_value", oldVal).
		Str("new_value", newVal).
		Uint("user_id", userID).
		Msg("系统设置重置为默认值")
	respondMessage(c, "设置已重置")
}
