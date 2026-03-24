package handlers

import (
	"net/http"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type WSHandler struct {
	hub        *ws.Hub
	jwtManager *auth.JWTManager
	db         *gorm.DB
}

func NewWSHandler(hub *ws.Hub, jwtManager *auth.JWTManager, db *gorm.DB) *WSHandler {
	return &WSHandler{hub: hub, jwtManager: jwtManager, db: db}
}

func (h *WSHandler) ServeWS(c *gin.Context) {
	if h.hub == nil || h.jwtManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket 服务不可用"})
		return
	}
	// WebSocket 无法通过 HTTP 头传递 JWT，认证在升级后通过首条消息完成。
	// 此处同时校验 token 有效性和 RBAC 权限（tasks:read）。
	h.hub.ServeWS(c, func(token string) bool {
		_, err := authorizeRealtimeToken(token, h.jwtManager, h.db, realtimeAuthRequirements{Permission: "tasks:read"})
		if err != nil {
			return false
		}
		return true
	})
}
