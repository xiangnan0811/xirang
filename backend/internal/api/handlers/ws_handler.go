package handlers

import (
	"net/http"

	"xirang/backend/internal/auth"
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
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 token"})
		return
	}
	if _, err := h.jwtManager.ParseToken(token); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token 无效或过期"})
		return
	}
	if h.hub == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket 服务不可用"})
		return
	}
	h.hub.ServeWS(c)
}
