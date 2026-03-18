package handlers

import (
	"net/http"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
)

// SettingsHandler 系统设置接口
type SettingsHandler struct {
	svc *settings.Service
}

// NewSettingsHandler 创建设置处理器
func NewSettingsHandler(svc *settings.Service) *SettingsHandler {
	return &SettingsHandler{svc: svc}
}

// GetAll GET /settings — 返回设置定义 + 当前值
func (h *SettingsHandler) GetAll(c *gin.Context) {
	result, err := h.svc.GetAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询设置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"definitions": h.svc.Registry(),
			"values":      result,
		},
	})
}

// BatchUpdate PUT /settings — 批量更新设置
func (h *SettingsHandler) BatchUpdate(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	userID := c.GetUint(middleware.CtxUserID)

	for key, value := range req {
		oldVal := h.svc.GetEffective(key)
		if err := h.svc.Update(key, value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logger.Module("audit").Info().
			Str("action", "settings_update").
			Str("key", key).
			Str("old_value", oldVal).
			Str("new_value", value).
			Str("source", "db").
			Uint("user_id", userID).
			Msg("系统设置变更")
	}

	c.JSON(http.StatusOK, gin.H{"message": "设置已更新"})
}

// Delete DELETE /settings/:key — 删除 DB 覆盖值（恢复默认）
func (h *SettingsHandler) Delete(c *gin.Context) {
	key := c.Param("key")
	if err := h.svc.Delete(key); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "设置已重置"})
}
