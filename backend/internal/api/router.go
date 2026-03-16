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
	router.Use(gin.Recovery(), middleware.RequestID(), middleware.StructuredLogger())
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

	captchaStore := handlers.NewCaptchaStore()
	captchaHandler := handlers.NewCaptchaHandler(captchaStore)
	authHandler := handlers.NewAuthHandler(dep.AuthService, dep.JWTManager, dep.LoginCaptchaEnabled, dep.LoginSecondCaptchaEnabled).WithDB(dep.DB).WithCaptchaStore(captchaStore)
	overviewHandler := handlers.NewOverviewHandler(dep.DB)
	overviewTrafficHandler := handlers.NewOverviewTrafficHandler(dep.DB, nil)
	backupHealthHandler := handlers.NewBackupHealthHandler(dep.DB)
	storageUsageHandler := handlers.NewStorageUsageHandler(dep.DB)
	nodeHandler := handlers.NewNodeHandler(dep.DB, dep.TaskManager)
	policyHandler := handlers.NewPolicyHandler(dep.DB, dep.TaskManager)
	taskHandler := handlers.NewTaskHandler(dep.DB, dep.TaskManager)
	taskRunHandler := handlers.NewTaskRunHandler(dep.DB)
	sshKeyHandler := handlers.NewSSHKeyHandler(dep.DB)
	integrationHandler := handlers.NewIntegrationHandler(dep.DB)
	alertHandler := handlers.NewAlertHandler(dep.DB)
	auditHandler := handlers.NewAuditHandler(dep.DB)
	userHandler := handlers.NewUserHandler(dep.AuthService)
	batchHandler := handlers.NewBatchHandler(dep.DB, dep.TaskManager)
	fileHandler := handlers.NewFileHandler(dep.DB)
	reportHandler := handlers.NewReportHandler(dep.DB)
	hookTemplatesHandler := handlers.NewHookTemplatesHandler()
	snapshotHandler := handlers.NewSnapshotHandler(dep.DB)
	configHandler := handlers.NewConfigHandler(dep.DB)
	wsHandler := handlers.NewWSHandler(dep.Hub, dep.JWTManager)
	terminalHandler := handlers.NewTerminalHandler(dep.DB, dep.JWTManager, dep.Hub.CheckOrigin)

	v1 := router.Group("/api/v1")
	v1.GET("/auth/captcha", captchaHandler.GenerateCaptcha)
	v1.POST("/auth/login", middleware.LoginRateLimitWithContext(appCtx, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.Login)
	v1.POST("/auth/2fa/login", middleware.LoginRateLimitWithContext(appCtx, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.TOTPLogin)

	secured := v1.Group("")
	secured.Use(middleware.AuthMiddleware(dep.JWTManager, dep.DB))
	secured.Use(middleware.AuditLogger(dep.DB))
	secured.GET("/me", authHandler.Me)
	secured.POST("/me/onboarded", authHandler.CompleteOnboarding)
	secured.POST("/auth/logout", authHandler.Logout)
	secured.POST("/auth/change-password", authHandler.ChangePassword)
	secured.POST("/auth/2fa/setup", authHandler.TOTPSetup)
	secured.POST("/auth/2fa/verify", authHandler.TOTPVerify)
	secured.POST("/auth/2fa/disable", authHandler.TOTPDisable)
	secured.GET("/overview", overviewHandler.Get)
	secured.GET("/overview/traffic", middleware.RBAC("tasks:read"), overviewTrafficHandler.Get)
	secured.GET("/overview/backup-health", middleware.RBAC("tasks:read"), backupHealthHandler.Get)
	secured.GET("/overview/storage-usage", middleware.RBAC("tasks:read"), storageUsageHandler.Get)
	secured.GET("/users", middleware.ETag(), middleware.RBAC("users:manage"), userHandler.List)
	secured.POST("/users", middleware.RBAC("users:manage"), userHandler.Create)
	secured.PUT("/users/:id", middleware.RBAC("users:manage"), userHandler.Update)
	secured.DELETE("/users/:id", middleware.RBAC("users:manage"), userHandler.Delete)

	secured.GET("/nodes", middleware.RBAC("nodes:read"), nodeHandler.List)
	secured.GET("/nodes/:id", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Get)
	secured.POST("/nodes", middleware.RBAC("nodes:write"), nodeHandler.Create)
	secured.POST("/nodes/batch-delete", middleware.RBAC("nodes:write"), nodeHandler.BatchDelete)
	secured.PUT("/nodes/:id", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Update)
	secured.DELETE("/nodes/:id", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Delete)
	secured.POST("/nodes/:id/test-connection", middleware.RBAC("nodes:test"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.TestConnection)
	secured.GET("/nodes/:id/metrics", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Metrics)
	secured.GET("/nodes/:id/files", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), fileHandler.ListNodeFiles)
	secured.GET("/nodes/:id/files/content", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), fileHandler.GetNodeFileContent)
	secured.GET("/nodes/:id/owners", middleware.RBAC("nodes:owners"), nodeHandler.ListOwners)
	secured.POST("/nodes/:id/owners", middleware.RBAC("nodes:owners"), nodeHandler.AddOwner)
	secured.DELETE("/nodes/:id/owners/:user_id", middleware.RBAC("nodes:owners"), nodeHandler.RemoveOwner)
	secured.POST("/nodes/:id/emergency-backup", middleware.RBAC("tasks:trigger"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.EmergencyBackup)

	secured.GET("/ssh-keys", middleware.ETag(), middleware.RBAC("ssh_keys:read"), sshKeyHandler.List)
	secured.GET("/ssh-keys/:id", middleware.RBAC("ssh_keys:read"), sshKeyHandler.Get)
	secured.POST("/ssh-keys", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Create)
	secured.PUT("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Update)
	secured.DELETE("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Delete)

	secured.GET("/integrations", middleware.ETag(), middleware.RBAC("integrations:read"), integrationHandler.List)
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
	secured.POST("/policies/batch-toggle", middleware.RBAC("policies:write"), policyHandler.BatchToggle)
	secured.POST("/policies/from-template/:id", middleware.RBAC("policies:write"), policyHandler.CloneFromTemplate)
	secured.PUT("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Update)
	secured.DELETE("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Delete)

	secured.GET("/tasks", middleware.RBAC("tasks:read"), taskHandler.List)
	secured.GET("/tasks/:id", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Get)
	secured.GET("/tasks/:id/logs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Logs)
	secured.POST("/tasks", middleware.RBAC("tasks:write"), taskHandler.Create)
	secured.PUT("/tasks/:id", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Update)
	secured.DELETE("/tasks/:id", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Delete)
	secured.GET("/tasks/:id/runs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskRunHandler.ListByTask)
	secured.POST("/tasks/batch-trigger", middleware.RBAC("tasks:write"), taskHandler.BatchTrigger)
	secured.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Trigger)
	secured.POST("/tasks/:id/cancel", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Cancel)
	secured.POST("/tasks/:id/restore", middleware.RequireRole("admin"), taskHandler.Restore)
	secured.GET("/tasks/:id/backup-files", middleware.RequireRole("admin"), fileHandler.ListTaskBackupFiles)

	secured.GET("/task-runs/:id", middleware.RBAC("tasks:read"), taskRunHandler.Get)
	secured.GET("/task-runs/:id/logs", middleware.RBAC("tasks:read"), taskRunHandler.Logs)

	secured.POST("/batch-commands", middleware.RBAC("tasks:write"), batchHandler.Create)
	secured.GET("/batch-commands/:batch_id", middleware.RBAC("tasks:read"), batchHandler.Get)
	secured.DELETE("/batch-commands/:batch_id", middleware.RBAC("tasks:write"), batchHandler.Delete)

	secured.GET("/report-configs", middleware.RBAC("reports:read"), reportHandler.ListConfigs)
	secured.POST("/report-configs", middleware.RBAC("reports:write"), reportHandler.CreateConfig)
	secured.PUT("/report-configs/:id", middleware.RBAC("reports:write"), reportHandler.UpdateConfig)
	secured.DELETE("/report-configs/:id", middleware.RBAC("reports:write"), reportHandler.DeleteConfig)
	secured.POST("/report-configs/:id/generate", middleware.RBAC("reports:write"), reportHandler.GenerateNow)
	secured.GET("/report-configs/:id/reports", middleware.RBAC("reports:read"), reportHandler.ListReports)
	secured.GET("/reports/:id", middleware.RBAC("reports:read"), reportHandler.GetReport)

	secured.GET("/hook-templates", middleware.RBAC("policies:read"), hookTemplatesHandler.List)

	secured.GET("/tasks/:id/snapshots", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotHandler.ListSnapshots)
	secured.GET("/tasks/:id/snapshots/:sid/files", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotHandler.ListFiles)
	secured.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), snapshotHandler.Restore)

	secured.GET("/config/export", middleware.RequireRole("admin"), configHandler.Export)
	secured.POST("/config/import", middleware.RequireRole("admin"), configHandler.Import)

	secured.POST("/nodes/:id/migrate", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Migrate)

	// WebSocket 路由放在 secured 外部：浏览器 WebSocket API 无法设置自定义 HTTP 头，
	// 因此无法通过 AuthMiddleware。认证改由 WS 协议内首条消息完成（含 RBAC 校验）。
	v1.GET("/ws/logs", wsHandler.ServeWS)
	v1.GET("/ws/terminal", terminalHandler.ServeTerminal)

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
