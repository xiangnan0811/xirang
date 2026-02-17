package handlers

import (
	"net/http"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
)

const (
	wsAuthTokenProtocolPrefix = "xirang-auth-token."
)

type WSHandler struct {
	hub        *ws.Hub
	jwtManager *auth.JWTManager
}

func NewWSHandler(hub *ws.Hub, jwtManager *auth.JWTManager) *WSHandler {
	return &WSHandler{hub: hub, jwtManager: jwtManager}
}

func (h *WSHandler) ServeWS(c *gin.Context) {
	token := extractWSToken(c)
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

func extractWSToken(c *gin.Context) string {
	return tokenFromSubprotocol(c.GetHeader("Sec-WebSocket-Protocol"))
}

func tokenFromSubprotocol(headerValue string) string {
	if headerValue == "" {
		return ""
	}

	for _, item := range strings.Split(headerValue, ",") {
		protocol := strings.TrimSpace(item)
		if strings.HasPrefix(protocol, wsAuthTokenProtocolPrefix) {
			token := strings.TrimPrefix(protocol, wsAuthTokenProtocolPrefix)
			if token != "" {
				return token
			}
		}
	}

	return ""
}
