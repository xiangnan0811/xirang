package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/integration"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/node"
	"xirang/backend/internal/policy"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task"
	"xirang/backend/internal/util"
	"xirang/backend/internal/ws"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"

	_ "xirang/backend/internal/api/docs"
)

type Dependencies struct {
	AppContext        context.Context
	DB                *gorm.DB
	AuthService       *auth.Service
	JWTManager        *auth.JWTManager
	TaskManager       *task.Manager
	Hub               *ws.Hub
	AllowedOrigins    []string
	LoginRateLimit    int
	LoginRateWindow   time.Duration
	SettingsService   *settings.Service
	RetryWorker       *alerting.RetryWorker
	AlertDispatcher   *alerting.Dispatcher
	MetricsToken      string
	MetricsRateLimit  int
	MetricsRateWindow time.Duration
}

func NewRouter(dep Dependencies) *gin.Engine {
	appCtx := dep.AppContext
	if appCtx == nil {
		appCtx = context.Background()
	}
	router := gin.New()
	router.MaxMultipartMemory = 10 << 20 // 10 MB
	router.Use(gin.Recovery(), middleware.RequestID(), middleware.StructuredLogger())
	router.Use(middleware.PrometheusMetrics())
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
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Xirang-Step-Up")
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
	authHandler := handlers.NewAuthHandler(dep.AuthService, dep.JWTManager, dep.SettingsService).WithDB(dep.DB).WithCaptchaStore(captchaStore)
	overviewHandler := handlers.NewOverviewHandler(dep.DB)
	overviewTrafficHandler := handlers.NewOverviewTrafficHandler(dep.DB, nil)
	healthIncidentTimelineHandler := handlers.NewHealthIncidentTimelineHandler(dep.DB)
	backupHealthHandler := handlers.NewBackupHealthHandler(dep.DB)
	backupConfidenceHandler := handlers.NewBackupConfidenceHandler(dep.DB)
	storageUsageHandler := handlers.NewStorageUsageHandler(dep.DB)
	nodeRepo := gormrepo.NewNodeRepository(dep.DB)
	policyRepo := gormrepo.NewPolicyRepository(dep.DB)
	taskRepo := gormrepo.NewTaskRepository(dep.DB)

	nodeSvc := node.NewNodeService(nodeRepo)
	nodeHandler := handlers.NewNodeHandler(dep.DB, dep.TaskManager, nodeSvc).WithSettingsService(dep.SettingsService).WithAlertDispatcher(dep.AlertDispatcher)
	policySvc := policy.NewPolicyService(policyRepo, dep.TaskManager)
	policyHandler := handlers.NewPolicyHandler(dep.DB, dep.TaskManager).WithPolicyService(policySvc)
	taskSvc := task.NewTaskApiService(taskRepo, nodeRepo, policyRepo, dep.TaskManager)
	taskHandler := handlers.NewTaskHandler(dep.DB, dep.TaskManager).WithTaskApiService(taskSvc).WithJWTManager(dep.JWTManager)
	taskRunHandler := handlers.NewTaskRunHandler(dep.DB)
	sshKeyHandler := handlers.NewSSHKeyHandler(dep.DB)
	integrationSvc := integration.NewIntegrationService(dep.DB)
	integrationHandler := handlers.NewIntegrationHandler(dep.DB, integrationSvc)
	alertHandler := handlers.NewAlertHandler(dep.DB)
	auditHandler := handlers.NewAuditHandler(dep.DB)
	credentialAuditHandler := handlers.NewCredentialAuditHandler(dep.DB)
	credentialAccessGrantHandler := handlers.NewCredentialAccessGrantHandler(dep.DB, dep.JWTManager)
	userHandler := handlers.NewUserHandler(dep.AuthService)
	batchHandler := handlers.NewBatchHandler(dep.DB, dep.TaskManager).WithJWTManager(dep.JWTManager)
	fileHandler := handlers.NewFileHandler(dep.DB)
	dockerHandler := handlers.NewDockerHandler(dep.DB)
	reportHandler := handlers.NewReportHandler(dep.DB)
	hookTemplatesHandler := handlers.NewHookTemplatesHandler()
	snapshotHandler := handlers.NewSnapshotHandler(dep.DB)
	snapshotDiffHandler := handlers.NewSnapshotDiffHandler(dep.DB)
	snapshotSearchHandler := handlers.NewSnapshotSearchHandler(dep.DB)
	configHandler := handlers.NewConfigHandler(dep.DB, dep.SettingsService)
	appCredentialHandler := handlers.NewAppCredentialHandler(dep.DB)
	settingsHandler := handlers.NewSettingsHandler(dep.DB, dep.SettingsService)
	versionHandler := handlers.NewVersionHandler()
	systemHandler := handlers.NewSystemHandler(dep.DB)
	storageGuideHandler := handlers.NewStorageGuideHandler()
	wsHandler := handlers.NewWSHandler(dep.Hub, dep.JWTManager, dep.DB)
	terminalHandler := handlers.NewTerminalHandler(dep.DB, dep.JWTManager, dep.Hub.CheckOrigin)

	v1 := router.Group("/api/v1")
	v1.GET("/auth/captcha", captchaHandler.GenerateCaptcha)
	v1.POST("/auth/login", middleware.LoginRateLimitWithContext(appCtx, dep.SettingsService, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.Login)
	v1.POST("/auth/2fa/login", middleware.LoginRateLimitWithContext(appCtx, dep.SettingsService, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.TOTPLogin)
	v1.GET("/version", versionHandler.Info)
	secured := v1.Group("")
	secured.Use(middleware.AuthMiddleware(dep.JWTManager, dep.DB))
	secured.Use(middleware.AuditLogger(dep.DB))
	secured.Use(middleware.APIRateLimit(200, time.Minute))
	secured.Use(middleware.MaxBodySize(20 << 20)) // 20 MB
	secured.GET("/me", authHandler.Me)
	secured.POST("/me/onboarded", authHandler.CompleteOnboarding)
	secured.POST("/auth/logout", authHandler.Logout)
	secured.POST("/auth/change-password", authHandler.ChangePassword)
	secured.POST("/auth/2fa/setup", authHandler.TOTPSetup)
	secured.POST("/auth/2fa/verify", authHandler.TOTPVerify)
	secured.POST("/auth/2fa/disable", authHandler.TOTPDisable)
	secured.POST("/auth/step-up", authHandler.StepUp)
	secured.GET("/overview", overviewHandler.Get)
	secured.GET("/overview/traffic", middleware.RBAC("tasks:read"), overviewTrafficHandler.Get)
	secured.GET("/overview/health-incident-timeline", middleware.RBAC("tasks:read"), healthIncidentTimelineHandler.Get)
	secured.GET("/overview/backup-health", middleware.RBAC("tasks:read"), backupHealthHandler.Get)
	secured.GET("/overview/backup-confidence", middleware.RBAC("tasks:read"), backupConfidenceHandler.Get)
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
	secured.POST("/nodes/:id/doctor", middleware.RBAC("nodes:test"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.RunDoctor)
	secured.GET("/nodes/:id/metrics", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Metrics)
	nodeMetricsHandler := handlers.NewNodeMetricsHandler(dep.DB)
	secured.GET("/nodes/:id/status", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.Status)
	secured.GET("/nodes/:id/metric-series", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.Metrics)
	secured.GET("/nodes/:id/disk-forecast", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), nodeMetricsHandler.DiskForecast)
	secured.GET("/nodes/:id/files", middleware.RBAC("nodes:files"), middleware.OwnershipNodeCheck(dep.DB), fileHandler.ListNodeFiles)
	secured.GET("/nodes/:id/files/content", middleware.RBAC("nodes:files"), middleware.OwnershipNodeCheck(dep.DB), fileHandler.GetNodeFileContent)
	secured.GET("/nodes/:id/docker-volumes", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), dockerHandler.ListVolumes)
	secured.GET("/nodes/:id/owners", middleware.RBAC("nodes:owners"), nodeHandler.ListOwners)
	secured.POST("/nodes/:id/owners", middleware.RBAC("nodes:owners"), nodeHandler.AddOwner)
	secured.DELETE("/nodes/:id/owners/:user_id", middleware.RBAC("nodes:owners"), nodeHandler.RemoveOwner)
	secured.POST("/nodes/:id/emergency-backup", middleware.RBAC("tasks:trigger"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.EmergencyBackup)

	logCfgHandler := handlers.NewNodeLogConfigHandler(dep.DB)
	secured.GET("/nodes/:id/log-config", middleware.RBAC("logs:read"), middleware.OwnershipNodeCheck(dep.DB), logCfgHandler.Get)
	secured.PATCH("/nodes/:id/log-config", middleware.RBAC("logs:write"), middleware.OwnershipNodeCheck(dep.DB), logCfgHandler.Patch)

	nodeLogsHandler := handlers.NewNodeLogsHandler(dep.DB, dep.SettingsService)
	secured.GET("/node-logs", middleware.RBAC("logs:read"), nodeLogsHandler.Query)
	secured.GET("/alerts/:id/logs", middleware.RBAC("alerts:read"), nodeLogsHandler.AlertLogs)
	secured.GET("/settings/logs", middleware.RequireRole("admin"), nodeLogsHandler.GetSettings)
	secured.PATCH("/settings/logs", middleware.RequireRole("admin"), nodeLogsHandler.PatchSettings)

	dashboardHandler := handlers.NewDashboardHandler(dep.DB)
	secured.GET("/dashboards", middleware.RBAC("dashboards:read"), dashboardHandler.List)
	secured.POST("/dashboards", middleware.RBAC("dashboards:write"), dashboardHandler.Create)
	secured.GET("/dashboards/:id", middleware.RBAC("dashboards:read"), dashboardHandler.Get)
	secured.PATCH("/dashboards/:id", middleware.RBAC("dashboards:write"), dashboardHandler.Update)
	secured.DELETE("/dashboards/:id", middleware.RBAC("dashboards:write"), dashboardHandler.Delete)

	secured.POST("/dashboards/:id/panels", middleware.RBAC("dashboards:write"), dashboardHandler.AddPanel)
	secured.PATCH("/dashboards/:id/panels/:pid", middleware.RBAC("dashboards:write"), dashboardHandler.UpdatePanel)
	secured.DELETE("/dashboards/:id/panels/:pid", middleware.RBAC("dashboards:write"), dashboardHandler.DeletePanel)
	secured.PUT("/dashboards/:id/panels/layout", middleware.RBAC("dashboards:write"), dashboardHandler.UpdateLayout)

	panelQueryHandler := handlers.NewPanelQueryHandler(dep.DB)
	secured.POST("/dashboards/panel-query", middleware.RBAC("dashboards:read"), panelQueryHandler.Query)
	secured.GET("/dashboards/metrics", middleware.RBAC("dashboards:read"), panelQueryHandler.ListMetrics)

	escalationHandler := handlers.NewEscalationHandler(dep.DB)
	secured.GET("/escalation-policies", middleware.RBAC("escalation:read"), escalationHandler.List)
	secured.POST("/escalation-policies", middleware.RBAC("escalation:write"), escalationHandler.Create)
	secured.GET("/escalation-policies/:id", middleware.RBAC("escalation:read"), escalationHandler.Get)
	secured.PATCH("/escalation-policies/:id", middleware.RBAC("escalation:write"), escalationHandler.Update)
	secured.DELETE("/escalation-policies/:id", middleware.RBAC("escalation:write"), escalationHandler.Delete)

	secured.GET("/alerts/:id/escalation-events", middleware.RBAC("alerts:read"), alertHandler.EscalationEvents)

	serviceMonitorHandler := handlers.NewServiceMonitorHandler(dep.DB)
	secured.GET("/service-monitors", middleware.RBAC("service_monitors:read"), serviceMonitorHandler.List)
	secured.GET("/service-monitors/:id", middleware.RBAC("service_monitors:read"), serviceMonitorHandler.Get)
	secured.POST("/service-monitors", middleware.RBAC("service_monitors:write"), serviceMonitorHandler.Create)
	secured.PUT("/service-monitors/:id", middleware.RBAC("service_monitors:write"), serviceMonitorHandler.Update)
	secured.DELETE("/service-monitors/:id", middleware.RBAC("service_monitors:write"), serviceMonitorHandler.Delete)

	automationRuleHandler := handlers.NewAutomationRuleHandler(dep.DB)
	secured.GET("/automation-rules", middleware.RBAC("automation:read"), automationRuleHandler.List)
	secured.POST("/automation-rules", middleware.RBAC("automation:write"), automationRuleHandler.Create)
	secured.GET("/automation-rules/:id", middleware.RBAC("automation:read"), automationRuleHandler.Get)
	secured.PUT("/automation-rules/:id", middleware.RBAC("automation:write"), automationRuleHandler.Update)
	secured.DELETE("/automation-rules/:id", middleware.RBAC("automation:write"), automationRuleHandler.Delete)

	anomalyHandler := handlers.NewAnomalyHandler(dep.DB)
	secured.GET("/anomaly-events", middleware.RBAC("nodes:read"), anomalyHandler.List)
	secured.GET("/nodes/:id/anomaly-events", middleware.RBAC("nodes:read"), middleware.OwnershipNodeCheck(dep.DB), anomalyHandler.ListForNode)

	secured.GET("/ssh-keys", middleware.ETag(), middleware.RBAC("ssh_keys:read"), sshKeyHandler.List)
	secured.POST("/ssh-keys", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Create)
	secured.POST("/ssh-keys/batch", middleware.RBAC("ssh_keys:write"), sshKeyHandler.BatchCreate)
	secured.POST("/ssh-keys/batch-delete", middleware.RBAC("ssh_keys:write"), sshKeyHandler.BatchDelete)
	secured.GET("/ssh-keys/export", middleware.RBAC("ssh_keys:read"), handlers.RequireStepUp(dep.DB, dep.JWTManager, "ssh_key.export", sshutil.PurposeSSHKeyExport, "ssh_export"), sshKeyHandler.Export)
	secured.GET("/ssh-keys/:id", middleware.RBAC("ssh_keys:read"), sshKeyHandler.Get)
	secured.PUT("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Update)
	secured.DELETE("/ssh-keys/:id", middleware.RBAC("ssh_keys:write"), sshKeyHandler.Delete)
	secured.POST("/ssh-keys/:id/test-connection", middleware.RBAC("ssh_keys:write"), sshKeyHandler.TestConnection)

	secured.GET("/integrations", middleware.ETag(), middleware.RBAC("integrations:read"), integrationHandler.List)
	secured.GET("/integrations/:id", middleware.RBAC("integrations:read"), integrationHandler.Get)
	secured.POST("/integrations", middleware.RBAC("integrations:write"), integrationHandler.Create)
	secured.PUT("/integrations/:id", middleware.RBAC("integrations:write"), integrationHandler.Update)
	secured.PATCH("/integrations/:id", middleware.RBAC("integrations:write"), integrationHandler.Patch)
	secured.POST("/integrations/:id/test", middleware.RBAC("integrations:write"), integrationHandler.Test)
	secured.DELETE("/integrations/:id", middleware.RBAC("integrations:write"), integrationHandler.Delete)

	secured.GET("/app-credentials", middleware.RBAC("app_credentials:read"), appCredentialHandler.List)
	secured.GET("/app-credentials/profiles", middleware.RBAC("app_credentials:read"), appCredentialHandler.ListProfiles)
	secured.GET("/app-credentials/:id", middleware.RBAC("app_credentials:read"), appCredentialHandler.Get)
	secured.POST("/app-credentials", middleware.RBAC("app_credentials:write"), appCredentialHandler.Create)
	secured.PUT("/app-credentials/:id", middleware.RBAC("app_credentials:write"), appCredentialHandler.Update)
	secured.DELETE("/app-credentials/:id", middleware.RBAC("app_credentials:write"), appCredentialHandler.Delete)

	secured.GET("/alerts", middleware.RBAC("alerts:read"), alertHandler.List)
	secured.GET("/alerts/unread-count", middleware.RBAC("alerts:read"), alertHandler.UnreadCount)
	secured.GET("/alerts/:id", middleware.RBAC("alerts:read"), alertHandler.Get)
	secured.GET("/alerts/:id/group-info", middleware.RBAC("alerts:read"), alertHandler.GroupInfo)
	secured.GET("/alerts/delivery-stats", middleware.RBAC("alerts:deliveries"), alertHandler.DeliveryStats)
	secured.GET("/alerts/:id/deliveries", middleware.RBAC("alerts:deliveries"), alertHandler.Deliveries)
	secured.POST("/alerts/bulk-resolve", middleware.RBAC("alerts:write"), alertHandler.BulkResolve)
	secured.POST("/alerts/:id/ack", middleware.RBAC("alerts:write"), alertHandler.Ack)
	secured.POST("/alerts/:id/resolve", middleware.RBAC("alerts:write"), alertHandler.Resolve)
	secured.POST("/alerts/:id/retry-delivery", middleware.RBAC("alerts:write"), alertHandler.RetryDelivery)
	secured.POST("/alerts/:id/retry-failed-deliveries", middleware.RBAC("alerts:write"), alertHandler.RetryFailedDeliveries)
	secured.GET("/audit-logs", middleware.RBAC("audit:read"), auditHandler.List)
	secured.GET("/audit-logs/export", middleware.RBAC("audit:read"), auditHandler.ExportCSV)
	secured.GET("/credential-audit-events", middleware.RequireRole("admin"), credentialAuditHandler.List)
	secured.GET("/credential-audit-events/export", middleware.RequireRole("admin"), credentialAuditHandler.ExportCSV)
	secured.GET("/credential-access-grants", middleware.RequireRole("admin"), credentialAccessGrantHandler.List)
	secured.POST("/credential-access-grants/terminal", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestTerminalGrant)
	secured.POST("/credential-access-grants/config-import", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestConfigImportGrant)
	secured.POST("/credential-access-grants/config-export", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestConfigExportGrant)
	secured.POST("/credential-access-grants/snapshot-restore", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestSnapshotRestoreGrant)
	secured.POST("/credential-access-grants/task-restore", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestTaskRestoreGrant)
	secured.POST("/credential-access-grants/task-manual-trigger", middleware.RBAC("tasks:trigger"), credentialAccessGrantHandler.RequestTaskManualTriggerGrant)
	secured.POST("/credential-access-grants/task-batch-trigger", middleware.RBAC("tasks:write"), credentialAccessGrantHandler.RequestTaskBatchTriggerGrant)
	secured.POST("/credential-access-grants/batch-command", middleware.RBAC("tasks:write"), credentialAccessGrantHandler.RequestBatchCommandGrant)

	secured.GET("/policies", middleware.RBAC("policies:read"), policyHandler.List)
	secured.GET("/policies/:id", middleware.RBAC("policies:read"), policyHandler.Get)
	secured.POST("/policies", middleware.RBAC("policies:write"), policyHandler.Create)
	secured.POST("/policies/batch-toggle", middleware.RBAC("policies:write"), policyHandler.BatchToggle)
	secured.POST("/policies/from-template/:id", middleware.RBAC("policies:write"), policyHandler.CloneFromTemplate)
	secured.PUT("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Update)
	secured.DELETE("/policies/:id", middleware.RBAC("policies:write"), policyHandler.Delete)
	secured.POST("/policies/:id/drill-trigger", middleware.RBAC("tasks:trigger"), policyHandler.TriggerDrill)

	secured.GET("/tasks", middleware.RBAC("tasks:read"), taskHandler.List)
	secured.GET("/tasks/:id", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Get)
	secured.GET("/tasks/:id/logs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Logs)
	secured.POST("/tasks", middleware.RBAC("tasks:write"), taskHandler.Create)
	secured.PUT("/tasks/:id", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Update)
	secured.DELETE("/tasks/:id", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Delete)
	secured.GET("/tasks/:id/runs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskRunHandler.ListByTask)
	secured.POST("/tasks/batch-trigger", middleware.RBAC("tasks:write"), taskHandler.BatchTrigger)
	secured.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(dep.DB), handlers.RequireStepUp(dep.DB, dep.JWTManager, handlers.CredentialGrantActionTaskManualTrigger, sshutil.PurposeTaskCommand, "task_run"), handlers.RequireTaskManualTriggerCredentialGrant(dep.DB), taskHandler.Trigger)
	secured.POST("/tasks/:id/cancel", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Cancel)
	secured.POST("/tasks/:id/pause", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Pause)
	secured.POST("/tasks/:id/resume", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Resume)
	secured.POST("/tasks/:id/skip-next", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.SkipNext)
	secured.POST("/tasks/:id/restore", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, handlers.CredentialGrantActionTaskRestore, sshutil.PurposeTaskRestore, "task_restore"), handlers.RequireTaskRestoreCredentialGrant(dep.DB), taskHandler.Restore)
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
	secured.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, handlers.CredentialGrantActionSnapshotRestore, sshutil.PurposeSnapshot, "snapshot_restore"), handlers.RequireSnapshotRestoreCredentialGrant(dep.DB), snapshotHandler.Restore)
	secured.GET("/tasks/:id/snapshots/diff", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotDiffHandler.Diff)
	secured.GET("/tasks/:id/snapshots/search", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotSearchHandler.Search)

	secured.GET("/settings", middleware.RequireRole("admin"), settingsHandler.GetAll)
	secured.GET("/settings/security-risk-summary", middleware.RequireRole("admin"), settingsHandler.SecurityRiskSummary)
	secured.PUT("/settings", middleware.RequireRole("admin"), settingsHandler.BatchUpdate)
	secured.DELETE("/settings/:key", middleware.RequireRole("admin"), settingsHandler.Delete)

	secured.GET("/config/export", middleware.RequireRole("admin"), handlers.RequireStepUpIf(dep.DB, dep.JWTManager, handlers.CredentialGrantActionConfigExport, handlers.CredentialGrantPurposeConfigExport, "settings_export_sensitive", func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), handlers.RequireConfigExportCredentialGrantIf(dep.DB, func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), configHandler.Export)
	secured.POST("/config/import", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, handlers.CredentialGrantActionConfigImport, handlers.CredentialGrantPurposeConfigImport, "settings_import"), handlers.RequireConfigImportCredentialGrant(dep.DB), configHandler.Import)

	silenceHandler := handlers.NewSilenceHandler(dep.DB)
	// Writes are admin-only per P5b spec — silences are a platform-level ops
	// tool, not a per-operator self-serve feature.
	secured.GET("/silences", middleware.RBAC("alerts:read"), silenceHandler.List)
	secured.GET("/silences/:id", middleware.RBAC("alerts:read"), silenceHandler.Get)
	secured.POST("/silences", middleware.RequireRole("admin"), silenceHandler.Create)
	secured.PATCH("/silences/:id", middleware.RequireRole("admin"), silenceHandler.Patch)
	secured.DELETE("/silences/:id", middleware.RequireRole("admin"), silenceHandler.Delete)

	sloHandler := handlers.NewSLOHandler(dep.DB)
	secured.GET("/slos", middleware.RBAC("alerts:read"), sloHandler.List)
	secured.GET("/slos/compliance-summary", middleware.RBAC("alerts:read"), sloHandler.ComplianceSummary)
	secured.GET("/slos/:id", middleware.RBAC("alerts:read"), sloHandler.Get)
	secured.GET("/slos/:id/compliance", middleware.RBAC("alerts:read"), sloHandler.Compliance)
	secured.POST("/slos", middleware.RequireRole("admin"), sloHandler.Create)
	secured.PATCH("/slos/:id", middleware.RequireRole("admin"), sloHandler.Update)
	secured.DELETE("/slos/:id", middleware.RequireRole("admin"), sloHandler.Delete)

	if dep.RetryWorker != nil {
		alertDeliveryHandler := handlers.NewAlertDeliveryHandler(dep.RetryWorker)
		// Manual delivery retry is admin-only per P5b spec.
		secured.POST("/alert-deliveries/:id/retry", middleware.RequireRole("admin"), alertDeliveryHandler.Retry)
	}

	adminMetricsHandler := handlers.NewAdminMetricsHandler(dep.DB)
	secured.GET("/version/check", middleware.RequireRole("admin"), versionHandler.Check)
	secured.POST("/system/backup-db", middleware.RequireRole("admin"), systemHandler.BackupDB)
	secured.GET("/system/backups", middleware.RequireRole("admin"), systemHandler.ListBackups)
	secured.GET("/system/encryption-status", middleware.RequireRole("admin"), systemHandler.EncryptionStatus)
	secured.POST("/system/verify-mount", middleware.RequireRole("admin"), storageGuideHandler.VerifyMount)
	secured.GET("/admin/metrics/rollup-status", middleware.RequireRole("admin"), adminMetricsHandler.RollupStatus)

	secured.POST("/nodes/:id/migrate", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.Migrate)
	secured.POST("/nodes/:id/migrate/preflight", middleware.RBAC("nodes:write"), middleware.OwnershipNodeCheck(dep.DB), nodeHandler.MigratePreflight)

	// Status Page 无需认证的公开端点
	v1.GET("/status-page", serviceMonitorHandler.StatusPage)

	// WebSocket 路由放在 secured 外部：浏览器 WebSocket API 无法设置自定义 HTTP 头，
	// 因此无法通过 AuthMiddleware。认证改由 WS 协议内首条消息完成（含 RBAC 校验）。
	v1.GET("/ws/logs", wsHandler.ServeWS)
	v1.GET("/ws/terminal", terminalHandler.ServeTerminal)

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	metricsRateLimit := dep.MetricsRateLimit
	if metricsRateLimit <= 0 {
		metricsRateLimit = 5
	}
	metricsRateWindow := dep.MetricsRateWindow
	if metricsRateWindow <= 0 {
		metricsRateWindow = time.Second
	}
	router.GET("/metrics",
		middleware.MetricsRateLimit(metricsRateLimit, metricsRateWindow),
		middleware.MetricsAuth(dep.MetricsToken),
		gin.WrapH(promhttp.Handler()),
	)
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

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
