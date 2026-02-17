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
	if h.hub == nil || h.jwtManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket 服务不可用"})
		return
	}
	h.hub.ServeWS(c, func(token string) bool {
		_, err := h.jwtManager.ParseToken(token)
		return err == nil
	})
}
