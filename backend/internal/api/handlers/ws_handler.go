package handlers

import (
	"net/http"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
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
	// 此处同时校验 token、RBAC 权限以及 operator 的对象级可见性边界。
	h.hub.ServeWS(c, func(token string) (ws.AccessScope, error) {
		claims, err := authorizeRealtimeToken(token, h.jwtManager, h.db, realtimeAuthRequirements{Permission: "tasks:read"})
		if err != nil {
			return ws.AccessScope{}, err
		}
		access := ws.AccessScope{Role: claims.Role}
		if claims.Role != "operator" || h.db == nil {
			return access, nil
		}

		nodeIDs, err := middleware.OwnedNodeIDs(h.db, claims.UserID)
		if err != nil {
			return ws.AccessScope{}, err
		}
		access.AllowedNodeIDs = make(map[uint]struct{}, len(nodeIDs))
		for _, nodeID := range nodeIDs {
			access.AllowedNodeIDs[nodeID] = struct{}{}
		}
		return access, nil
	})
}
