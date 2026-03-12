package handlers

import (
	"net/http"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
)

type WSHandler struct {
	hub        *ws.Hub
	jwtManager *auth.JWTManager
}

func NewWSHandler(hub *ws.Hub, jwtManager *auth.JWTManager) *WSHandler {
	return &WSHandler{hub: hub, jwtManager: jwtManager}
}

func (h *WSHandler) ServeWS(c *gin.Context) {
	if h.hub == nil || h.jwtManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket 服务不可用"})
		return
	}
	// WebSocket 无法通过 HTTP 头传递 JWT，认证在升级后通过首条消息完成。
	// 此处同时校验 token 有效性和 RBAC 权限（tasks:read）。
	h.hub.ServeWS(c, func(token string) bool {
		claims, err := h.jwtManager.ParseToken(token)
		if err != nil {
			return false
		}
		return middleware.HasPermission(claims.Role, "tasks:read")
	})
}
