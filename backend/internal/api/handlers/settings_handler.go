package handlers

import (
	"net/http"

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

// BatchUpdate PUT /settings — 批量更新设置（原子操作：先校验全部，再统一写入）
func (h *SettingsHandler) BatchUpdate(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	// 预检：校验全部 key/value，不写入
	for key, value := range req {
		if err := h.svc.Validate(key, value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存设置失败"})
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

	c.JSON(http.StatusOK, gin.H{"message": "设置已更新"})
}

// Delete DELETE /settings/:key — 删除 DB 覆盖值（恢复默认），含审计日志
func (h *SettingsHandler) Delete(c *gin.Context) {
	key := c.Param("key")
	oldVal := h.svc.GetEffective(key)
	if err := h.svc.Delete(key); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	c.JSON(http.StatusOK, gin.H{"message": "设置已重置"})
}
