package api

import (
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/task"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Dependencies struct {
	DB                        *gorm.DB
	AuthService               *auth.Service
	JWTManager                *auth.JWTManager
	TaskManager               *task.Manager
	Hub                       *ws.Hub
	AllowedOrigins            []string
	LoginRateLimit            int
	LoginRateWindow           time.Duration
	LoginCaptchaEnabled       bool
	LoginSecondCaptchaEnabled bool
}

func NewRouter(dep Dependencies) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowedOrigin := resolveAllowedOrigin(origin, dep.AllowedOrigins)
		if allowedOrigin == "" && origin != "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "origin not allowed"})
			c.Abort()
			return
		}
		if allowedOrigin != "" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
		c.Writer.Header().Set("X-Frame-Options", "DENY")
		c.Writer.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Writer.Header().Set("X-XSS-Protection", "1; mode=block")
		c.Next()
	})

	authHandler := handlers.NewAuthHandler(dep.AuthService, dep.LoginCaptchaEnabled, dep.LoginSecondCaptchaEnabled)
	overviewHandler := handlers.NewOverviewHandler(dep.DB)
	nodeHandler := handlers.NewNodeHandler(dep.DB)
	policyHandler := handlers.NewPolicyHandler(dep.DB)
	taskHandler := handlers.NewTaskHandler(dep.DB, dep.TaskManager)
	sshKeyHandler := handlers.NewSSHKeyHandler(dep.DB)
	integrationHandler := handlers.NewIntegrationHandler(dep.DB)
	alertHandler := handlers.NewAlertHandler(dep.DB)
	auditHandler := handlers.NewAuditHandler(dep.DB)
	wsHandler := handlers.NewWSHandler(dep.Hub, dep.JWTManager)

	v1 := router.Group("/api/v1")
	v1.POST("/auth/login", middleware.LoginRateLimit(dep.LoginRateLimit, dep.LoginRateWindow), authHandler.Login)

	secured := v1.Group("")
	secured.Use(middleware.AuthMiddleware(dep.JWTManager))
	secured.Use(middleware.AuditLogger(dep.DB))
	secured.GET("/me", authHandler.Me)
	secured.GET("/overview", overviewHandler.Get)

	secured.GET("/nodes", middleware.RBAC("nodes:read"), nodeHandler.List)
	secured.GET("/nodes/:id", middleware.RBAC("nodes:read"), nodeHandler.Get)
	secured.POST("/nodes", middleware.RBAC("nodes:write"), nodeHandler.Create)
	secured.POST("/nodes/batch-delete", middleware.RBAC("nodes:write"), nodeHandler.BatchDelete)
	secured.PUT("/nodes/:id", middleware.RBAC("nodes:write"), nodeHandler.Update)
	secured.DELETE("/nodes/:id", middleware.RBAC("nodes:write"), nodeHandler.Delete)
	secured.POST("/nodes/:id/test-connection", middleware.RBAC("nodes:test"), nodeHandler.TestConnection)

	secured.GET("/ssh-keys", middleware.RBAC("ssh_keys:read"), sshKeyHandler.List)
	secured.GET("/ssh-keys/:id", middleware.RBAC("ssh_keys:read"), sshKeyHandler.Get)
	secured.POST("/ssh-keys", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Create)
	secured.PUT("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Update)
	secured.DELETE("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Delete)

	secured.GET("/integrations", middleware.RBAC("integrations:read"), integrationHandler.List)
	secured.GET("/integrations/:id", middleware.RBAC("integrations:read"), integrationHandler.Get)
	secured.POST("/integrations", middleware.RBAC("integrations:write"), integrationHandler.Create)
	secured.PUT("/integrations/:id", middleware.RBAC("integrations:write"), integrationHandler.Update)
	secured.POST("/integrations/:id/test", middleware.RBAC("integrations:write"), integrationHandler.Test)
	secured.DELETE("/integrations/:id", middleware.RBAC("integrations:write"), integrationHandler.Delete)

	secured.GET("/alerts", middleware.RBAC("alerts:read"), alertHandler.List)
	secured.GET("/alerts/:id", middleware.RBAC("alerts:read"), alertHandler.Get)
	secured.GET("/alerts/delivery-stats", middleware.RBAC("alerts:deliveries"), alertHandler.DeliveryStats)
	secured.GET("/alerts/:id/deliveries", middleware.RBAC("alerts:deliveries"), alertHandler.Deliveries)
	secured.POST("/alerts/:id/ack", middleware.RBAC("alerts:write"), alertHandler.Ack)
	secured.POST("/alerts/:id/resolve", middleware.RBAC("alerts:write"), alertHandler.Resolve)
	secured.POST("/alerts/:id/retry-delivery", middleware.RBAC("alerts:write"), alertHandler.RetryDelivery)
	secured.POST("/alerts/:id/retry-failed-deliveries", middleware.RBAC("alerts:write"), alertHandler.RetryFailedDeliveries)
	secured.GET("/audit-logs", middleware.RBAC("audit:read"), auditHandler.List)
	secured.GET("/audit-logs/export", middleware.RBAC("audit:read"), auditHandler.ExportCSV)

	secured.GET("/policies", middleware.RBAC("policies:read"), policyHandler.List)
	secured.GET("/policies/:id", middleware.RBAC("policies:read"), policyHandler.Get)
	secured.POST("/policies", middleware.RBAC("policies:write"), policyHandler.Create)
	secured.PUT("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Update)
	secured.DELETE("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Delete)

	secured.GET("/tasks", middleware.RBAC("tasks:read"), taskHandler.List)
	secured.GET("/tasks/:id", middleware.RBAC("tasks:read"), taskHandler.Get)
	secured.GET("/tasks/:id/logs", middleware.RBAC("tasks:read"), taskHandler.Logs)
	secured.POST("/tasks", middleware.RBAC("tasks:write"), taskHandler.Create)
	secured.PUT("/tasks/:id", middleware.RBAC("tasks:write"), taskHandler.Update)
	secured.DELETE("/tasks/:id", middleware.RBAC("tasks:write"), taskHandler.Delete)
	secured.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), taskHandler.Trigger)
	secured.POST("/tasks/:id/cancel", middleware.RBAC("tasks:write"), taskHandler.Cancel)

	v1.GET("/ws/logs", wsHandler.ServeWS)

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	return router
}

func resolveAllowedOrigin(origin string, allowList []string) string {
	trimmedOrigin := strings.TrimSpace(origin)
	if len(allowList) == 0 {
		return ""
	}
	if trimmedOrigin == "" {
		return ""
	}
	for _, item := range allowList {
		trimmedItem := strings.TrimSpace(item)
		if trimmedItem == "*" {
			return trimmedOrigin
		}
		if strings.EqualFold(trimmedItem, trimmedOrigin) {
			return trimmedOrigin
		}
	}
	return ""
}
