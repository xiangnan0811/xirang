package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/task"
	"xirang/backend/internal/util"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Dependencies struct {
	AppContext                context.Context
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
	appCtx := dep.AppContext
	if appCtx == nil {
		appCtx = context.Background()
	}
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowedOrigin := resolveAllowedOrigin(origin, c.Request.Host, dep.AllowedOrigins)
		if allowedOrigin == "" && origin != "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "origin not allowed"})
			c.Abort()
			return
		}
		// 动态 CORS 回写时必须声明 Vary: Origin，防止中间缓存投毒。
		c.Writer.Header().Add("Vary", "Origin")
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
		csp := "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; connect-src 'self' wss:; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'"
		if util.IsDevelopmentEnv() {
			csp = "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'"
		}
		c.Writer.Header().Set("Content-Security-Policy", csp)
		c.Next()
	})

	authHandler := handlers.NewAuthHandler(dep.AuthService, dep.JWTManager, dep.LoginCaptchaEnabled, dep.LoginSecondCaptchaEnabled)
	overviewHandler := handlers.NewOverviewHandler(dep.DB)
	overviewTrafficHandler := handlers.NewOverviewTrafficHandler(dep.DB, nil)
	nodeHandler := handlers.NewNodeHandler(dep.DB)
	policyHandler := handlers.NewPolicyHandler(dep.DB, dep.TaskManager)
	taskHandler := handlers.NewTaskHandler(dep.DB, dep.TaskManager)
	sshKeyHandler := handlers.NewSSHKeyHandler(dep.DB)
	integrationHandler := handlers.NewIntegrationHandler(dep.DB)
	alertHandler := handlers.NewAlertHandler(dep.DB)
	auditHandler := handlers.NewAuditHandler(dep.DB)
	userHandler := handlers.NewUserHandler(dep.AuthService)
	wsHandler := handlers.NewWSHandler(dep.Hub, dep.JWTManager)

	v1 := router.Group("/api/v1")
	v1.POST("/auth/login", middleware.LoginRateLimitWithContext(appCtx, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.Login)

	secured := v1.Group("")
	secured.Use(middleware.AuthMiddleware(dep.JWTManager))
	secured.Use(middleware.AuditLogger(dep.DB))
	secured.GET("/me", authHandler.Me)
	secured.POST("/auth/logout", authHandler.Logout)
	secured.POST("/auth/change-password", authHandler.ChangePassword)
	secured.GET("/overview", overviewHandler.Get)
	secured.GET("/overview/traffic", middleware.RBAC("tasks:read"), overviewTrafficHandler.Get)
	secured.GET("/users", middleware.RBAC("users:manage"), userHandler.List)
	secured.POST("/users", middleware.RBAC("users:manage"), userHandler.Create)
	secured.PUT("/users/:id", middleware.RBAC("users:manage"), userHandler.Update)
	secured.DELETE("/users/:id", middleware.RBAC("users:manage"), userHandler.Delete)

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
	secured.GET("/alerts/unread-count", middleware.RBAC("alerts:read"), alertHandler.UnreadCount)
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

	// WebSocket 路由放在 secured 外部：浏览器 WebSocket API 无法设置自定义 HTTP 头，
	// 因此无法通过 AuthMiddleware。认证改由 WS 协议内首条消息完成（含 RBAC 校验）。
	v1.GET("/ws/logs", wsHandler.ServeWS)

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	return router
}

func resolveAllowedOrigin(origin string, requestHost string, allowList []string) string {
	trimmedOrigin := strings.TrimSpace(origin)
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

	// 当 Origin 与当前请求主机一致（忽略端口）时默认放行，避免局域网/公网同主机部署因端口差异误拦截。
	// 安全前提：浏览器保证 Host 头真实性；生产环境应通过反向代理强制设置 Host。
	if util.IsSameHostOrigin(trimmedOrigin, requestHost) {
		return trimmedOrigin
	}

	return ""
}
