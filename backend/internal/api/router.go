package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/integration"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/node"
	"xirang/backend/internal/policy"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/snapshot"
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
	AppContext            context.Context
	DB                    *gorm.DB
	AuthService           *auth.Service
	JWTManager            *auth.JWTManager
	TaskManager           *task.Manager
	Hub                   *ws.Hub
	AllowedOrigins        []string
	LoginRateLimit        int
	LoginRateWindow       time.Duration
	SettingsService       *settings.Service
	RetryWorker           *alerting.RetryWorker
	AlertDispatcher       *alerting.Dispatcher
	MetricsToken          string
	MetricsRateLimit      int
	MetricsRateWindow     time.Duration
	BackupAssets          *backupruntime.Runtime
	BackupContent         handlers.BackupContentService
	BackupContentConfig   handlers.BackupContentHandlerConfigSource
	LegacyResticSnapshots handlers.LegacyResticSnapshots
	SnapshotDiffRunner    handlers.SnapshotDiffRunner
	SnapshotIndexer       *snapshot.Indexer
	// TrustedProxies limits which reverse proxies may set X-Forwarded-For.
	// Empty = trust none (ClientIP uses RemoteAddr only).
	TrustedProxies []string
}

// featureDisabledBackupRepositoryService preserves the public feature gate
// when a lightweight Router construction intentionally omits the shared
// runtime. Production startup constructs and injects Runtime before Router.
// It owns no provider command or credential capability.
type featureDisabledBackupRepositoryService struct{}

func (featureDisabledBackupRepositoryService) Connect(_ context.Context, _ backuprepository.ConnectRequest, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	return backuprepository.ConnectResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) List(_ context.Context, _ backuprepository.RepositoryListRequest, _ backuprepository.VisibilityScope, requestContext backuprepository.RequestContext) (backuprepository.RepositoryPage, error) {
	return backuprepository.RepositoryPage{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) Detail(_ context.Context, _ string, _ backuprepository.VisibilityScope, requestContext backuprepository.RequestContext) (backuprepository.RepositoryView, error) {
	return backuprepository.RepositoryView{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) Reconcile(_ context.Context, _ string, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	return backuprepository.ConnectResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) Disconnect(_ context.Context, _ string, requestContext backuprepository.RequestContext) (backuprepository.ConnectResult, error) {
	return backuprepository.ConnectResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) DiscoverImportCandidates(_ context.Context, _ string, _ backuprepository.ImportDiscoveryRequest, requestContext backuprepository.RequestContext) (backuprepository.ImportDiscoveryResult, error) {
	return backuprepository.ImportDiscoveryResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) ListImportCandidates(_ context.Context, _ string, _ backuprepository.ImportCandidateListRequest, _ backuprepository.VisibilityScope, requestContext backuprepository.RequestContext) (backuprepository.ImportCandidatePage, error) {
	return backuprepository.ImportCandidatePage{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) ReviewImportCandidate(_ context.Context, _, _ string, _ backuprepository.ImportReviewRequest, requestContext backuprepository.RequestContext) (backuprepository.ImportCandidateView, error) {
	return backuprepository.ImportCandidateView{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledBackupRepositoryService) RebuildAcceptedImports(_ context.Context, _ string, _ backuprepository.RebuildRequest, requestContext backuprepository.RequestContext) (backuprepository.RebuildResult, error) {
	return backuprepository.RebuildResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func featureDisabledBackupRepositoryError(requestContext backuprepository.RequestContext) error {
	return &backuprepository.CapabilityError{
		Reason:        backupasset.CapabilityReason{Code: backupasset.CapabilityFeatureDisabled},
		CorrelationID: requestContext.CorrelationID,
	}
}

// featureDisabledRsyncVersioningService keeps the migration routes inert when
// a lightweight router lacks the shared backup runtime. It cannot derive or
// mutate any repository, Task, or Provider state.
type featureDisabledRsyncVersioningService struct{}

func (featureDisabledRsyncVersioningService) CreateRsyncVersioningPreflightForRequest(_ context.Context, _ backupasset.RsyncVersioningPreflightRequest, requestContext backuprepository.RequestContext) (backupasset.RsyncVersioningPreflightResult, error) {
	return backupasset.RsyncVersioningPreflightResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRsyncVersioningService) ActivateRsyncVersioningForRequest(_ context.Context, _ backupasset.RsyncVersioningActivationRequest, requestContext backuprepository.RequestContext) (backupasset.RsyncVersioningActivationResult, error) {
	return backupasset.RsyncVersioningActivationResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRsyncVersioningService) PrepareRsyncVersioningRollbackForRequest(_ context.Context, _ backupasset.RsyncVersioningRollbackPreparationRequest, requestContext backuprepository.RequestContext) (backupasset.RsyncVersioningRollbackPreparationResult, error) {
	return backupasset.RsyncVersioningRollbackPreparationResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRsyncVersioningService) RsyncVersioningSummary(_ context.Context, _ uint) (backupasset.RsyncVersioningSummary, error) {
	return backupasset.RsyncVersioningSummary{}, featureDisabledBackupRepositoryError(backuprepository.RequestContext{})
}

type featureDisabledRcloneVersioningService struct{}

func (featureDisabledRcloneVersioningService) CreateRclonePortableBindingSetupForRequest(_ context.Context, _ backupasset.RcloneBindingSetupRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	return backupasset.RcloneBindingSetupResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) SetRclonePortableBindingForRequest(_ context.Context, _ backupasset.RclonePortableBindingRequest, requestContext backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error) {
	return backupasset.RclonePublicationSummary{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) CreateRcloneNativeBindingSetupForRequest(_ context.Context, _ backupasset.RcloneBindingSetupRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error) {
	return backupasset.RcloneBindingSetupResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) SetRcloneNativeBindingForRequest(_ context.Context, _ backupasset.RcloneNativeBindingRequest, requestContext backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error) {
	return backupasset.RclonePublicationSummary{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) CreateRcloneVersioningPreflightForRequest(_ context.Context, _ backupasset.RcloneVersioningPreflightRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneVersioningPreflightResult, error) {
	return backupasset.RcloneVersioningPreflightResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) ActivateRcloneVersioningForRequest(_ context.Context, _ backupasset.RcloneVersioningActivationRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneVersioningActivationResult, error) {
	return backupasset.RcloneVersioningActivationResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) CleanRollbackRcloneVersioningForRequest(_ context.Context, _ backupasset.RcloneVersioningCleanRollbackRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	return backupasset.RcloneVersioningRollbackResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) PrepareRcloneVersioningRollbackForRequest(_ context.Context, _ backupasset.RcloneVersioningRollbackPreparationRequest, requestContext backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error) {
	return backupasset.RcloneVersioningRollbackResult{}, featureDisabledBackupRepositoryError(requestContext)
}

func (featureDisabledRcloneVersioningService) RcloneVersioningSummary(_ context.Context, _ uint) (backupasset.RclonePublicationSummary, error) {
	return backupasset.RclonePublicationSummary{}, featureDisabledBackupRepositoryError(backuprepository.RequestContext{})
}

func runtimeBackupAssetHandlerConfigSource(runtime *backupruntime.Runtime) handlers.BackupAssetHandlerConfigSource {
	return func() (handlers.BackupAssetHandlerConfig, error) {
		if runtime == nil || runtime.FoundationService() == nil {
			return handlers.BackupAssetHandlerConfig{}, fmt.Errorf("backup asset runtime config is unavailable")
		}
		live, err := runtime.FeatureLive()
		if err != nil {
			return handlers.BackupAssetHandlerConfig{}, err
		}
		searchConfig, overlayConfig, err := runtime.FoundationService().SearchOverlayConfig()
		if err != nil {
			return handlers.BackupAssetHandlerConfig{}, err
		}
		if searchConfig.Enabled != overlayConfig.Enabled {
			return handlers.BackupAssetHandlerConfig{}, fmt.Errorf("backup asset handler feature snapshot is inconsistent")
		}
		return handlers.BackupAssetHandlerConfig{
			Enabled: live,
			QueryLimits: assetsearch.QueryLimits{
				MaxBodyBytes: searchConfig.BodyMaxBytes, MaxDepth: searchConfig.ASTMaxDepth,
				MaxNodes: searchConfig.ASTMaxNodes, MaxValuesPerNode: searchConfig.ValuesPerNode,
				MaxValueBytes: searchConfig.ValueMaxBytes, MaxValueRunes: searchConfig.ValueMaxBytes,
				MaxPageSize: searchConfig.PageSizeMax, MaxCandidates: searchConfig.CandidateLimit,
				MaxExecutionTime: searchConfig.QueryTimeout, MaxSuggestions: searchConfig.SuggestionLimit,
			},
			IdempotencyKeyMaxBytes: overlayConfig.IdempotencyKeyMaxBytes,
		}, nil
	}
}

func NewRouter(dep Dependencies) *gin.Engine {
	appCtx := dep.AppContext
	if appCtx == nil {
		appCtx = context.Background()
	}
	router := gin.New()
	// Empty TrustedProxies → trust no proxy headers (prevents ClientIP spoofing
	// for rate limits). Non-empty → only listed CIDRs/IPs. Never leave Gin default
	// (trust all) which would honor forged X-Forwarded-For from any client.
	if err := router.SetTrustedProxies(dep.TrustedProxies); err != nil {
		// Fail closed: invalid TRUSTED_PROXIES must not silently degrade to
		// trust-none (wrong ClientIP breaks audit trails and rate limits) or
		// trust-all (XFF spoofing). Config.Load validates the list first.
		panic(fmt.Sprintf("TRUSTED_PROXIES invalid for Gin SetTrustedProxies: %v", err))
	}
	backupContentSchemePolicy, err := handlers.NewBackupContentSchemePolicy(dep.TrustedProxies)
	if err != nil {
		panic(fmt.Sprintf("TRUSTED_PROXIES invalid for backup content scheme policy: %v", err))
	}
	router.MaxMultipartMemory = 10 << 20 // 10 MB
	router.Use(gin.Recovery(), middleware.RequestID(), middleware.StructuredLogger())
	router.Use(middleware.PrometheusMetrics())
	router.Use(func(c *gin.Context) {
		if middleware.IsBackupContentShapedPath(c.Request.URL.Path) {
			c.Next()
			return
		}
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
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Xirang-Step-Up")
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
	captchaHandler := handlers.NewCaptchaHandler(captchaStore).WithSettingsService(dep.SettingsService)
	authHandler := handlers.NewAuthHandler(dep.AuthService, dep.JWTManager, dep.SettingsService).WithDB(dep.DB).WithCaptchaStore(captchaStore)
	backupContentService := dep.BackupContent
	if backupContentService == nil {
		backupContentService = handlers.NewFeatureDisabledBackupContentService()
	}
	backupContentConfig := dep.BackupContentConfig
	if backupContentConfig == nil {
		backupContentConfig = handlers.NewFeatureDisabledBackupContentHandlerConfigSource()
	}
	backupContentHandler := handlers.NewBackupContentHandler(
		backupContentService, dep.DB, dep.JWTManager, backupContentConfig,
	).WithSchemePolicy(backupContentSchemePolicy)
	var recoveryAuthorization handlers.RecoveryAuthorizationHandlerService
	var recoveryTargetRoots handlers.RecoveryTargetRootHandlerService
	var recoveryDowngrade handlers.RecoveryDowngradeHandlerService
	var recoveryLifecycle handlers.RecoveryLifecycleHandlerService
	var recoveryOperations handlers.RecoveryOperationsHandlerService
	if dep.BackupAssets != nil {
		recoveryAuthorization = dep.BackupAssets.RecoveryAuthorization()
		recoveryTargetRoots = dep.BackupAssets.RecoveryTargetRoots()
		recoveryDowngrade = dep.BackupAssets
		recoveryLifecycle = dep.BackupAssets.RecoveryAPI()
		recoveryOperations = dep.BackupAssets.RecoveryOperations()
	}
	backupRecoveryHandler := handlers.NewBackupRecoveryHandler(recoveryAuthorization, dep.DB, dep.JWTManager).
		WithRecoveryAdministration(recoveryTargetRoots, recoveryDowngrade).
		WithRecoveryLifecycle(recoveryLifecycle).
		WithRecoveryOperations(recoveryOperations)
	authHandler.WithContentSessionRevoker(backupContentService)
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
	integrationSvc := integration.NewIntegrationService(dep.DB).WithAlertDispatcher(dep.AlertDispatcher)
	integrationHandler := handlers.NewIntegrationHandler(dep.DB, integrationSvc)
	alertHandler := handlers.NewAlertHandler(dep.DB).WithAlertDispatcher(dep.AlertDispatcher)
	auditHandler := handlers.NewAuditHandler(dep.DB)
	credentialAuditHandler := handlers.NewCredentialAuditHandler(dep.DB)
	credentialAccessGrantHandler := handlers.NewCredentialAccessGrantHandler(dep.DB, dep.JWTManager)
	userHandler := handlers.NewUserHandler(dep.AuthService)
	batchHandler := handlers.NewBatchHandler(dep.DB, dep.TaskManager).WithJWTManager(dep.JWTManager)
	fileHandler := handlers.NewFileHandler(dep.DB)
	dockerHandler := handlers.NewDockerHandler(dep.DB)
	reportHandler := handlers.NewReportHandler(dep.DB)
	var backupRepositoryService handlers.BackupRepositoryService = featureDisabledBackupRepositoryService{}
	backupAssetCatalogService := handlers.NewFeatureDisabledBackupAssetCatalogService()
	backupFileSourceService := handlers.NewFeatureDisabledBackupFileSourceService()
	backupAssetSearchService := handlers.NewFeatureDisabledBackupAssetSearchService()
	backupAssetSavedSearchUseService := handlers.NewFeatureDisabledBackupAssetSavedSearchUseService()
	backupAssetOverlayService := handlers.NewFeatureDisabledBackupAssetOverlayService()
	backupAssetHandlerConfigSource := handlers.NewFeatureDisabledBackupAssetHandlerConfigSource()
	var backupAssetAuditSink handlers.BackupAssetAuditSink
	var lineageGuard publication.LineageGuard
	var featureTransitioner publication.FeatureTransitioner
	if dep.BackupAssets != nil {
		backupRepositoryService = dep.BackupAssets.RepositoryService()
		if dep.BackupAssets.CatalogService() != nil {
			catalogService := dep.BackupAssets.CatalogService()
			backupAssetCatalogService = catalogService
			backupFileSourceService = catalogService
			backupAssetAuditSink = dep.BackupAssets.CatalogAuditSink()
		}
		if dep.BackupAssets.SearchService() != nil {
			backupAssetSearchService = dep.BackupAssets.SearchService()
		}
		if dep.BackupAssets.OverlayService() != nil {
			backupAssetSavedSearchUseService = dep.BackupAssets.OverlayService()
			backupAssetOverlayService = dep.BackupAssets.OverlayService()
		}
		backupAssetHandlerConfigSource = runtimeBackupAssetHandlerConfigSource(dep.BackupAssets)
		lineageGuard = dep.BackupAssets.LineageGuard()
		featureTransitioner = dep.BackupAssets.FeatureTransitioner()
	}
	snapshotHandler := handlers.NewSnapshotHandler(dep.DB, lineageGuard, dep.LegacyResticSnapshots)
	if dep.BackupAssets != nil {
		snapshotHandler = snapshotHandler.WithFeatureLive(dep.BackupAssets.FeatureLive)
	}
	snapshotDiffHandler := handlers.NewSnapshotDiffHandler(dep.DB, lineageGuard, dep.SnapshotDiffRunner)
	snapshotSearchHandler := handlers.NewSnapshotSearchHandler(dep.DB, lineageGuard, dep.SnapshotIndexer)
	configHandler := handlers.NewConfigHandler(dep.DB, dep.SettingsService).WithBackupAssetTransitioner(featureTransitioner)
	appCredentialHandler := handlers.NewAppCredentialHandler(dep.DB)
	settingsHandler := handlers.NewSettingsHandler(dep.DB, dep.SettingsService).WithBackupAssetTransitioner(featureTransitioner)
	versionHandler := handlers.NewVersionHandler()
	systemHandler := handlers.NewSystemHandler(dep.DB)
	storageGuideHandler := handlers.NewStorageGuideHandler()
	wsHandler := handlers.NewWSHandler(dep.Hub, dep.JWTManager, dep.DB)
	terminalHandler := handlers.NewTerminalHandler(dep.DB, dep.JWTManager, dep.Hub.CheckOrigin)
	var backupRetentionPolicies handlers.BackupRetentionPolicyService
	var backupRetentionHolds handlers.BackupRetentionHoldService
	var backupRetentionPurge handlers.BackupRetentionPurgeService
	if dep.BackupAssets != nil {
		backupRetentionPolicies = handlers.NewRetentionPolicyHTTPService(dep.BackupAssets.RetentionPolicies())
		backupRetentionHolds = handlers.NewRetentionHoldHTTPService(dep.BackupAssets.RetentionHolds())
		backupRetentionPurge = handlers.NewRetentionPurgeHTTPService(dep.BackupAssets.RetentionPurge())
	}
	backupRetentionHandler := handlers.NewBackupRetentionHandler(
		backupRetentionPolicies, backupRetentionHolds, backupRetentionPurge, backupAssetAuditSink, backupAssetHandlerConfigSource,
	)
	backupRepositoryHandler := handlers.NewBackupRepositoryHandler(backupRepositoryService)
	backupAssetHandler := handlers.NewBackupAssetHandler(backupAssetCatalogService, backupAssetAuditSink)
	backupFileSourceHandler := handlers.NewBackupFileSourceHandler(backupFileSourceService, backupAssetAuditSink)
	backupAssetSearchHandler := handlers.NewBackupAssetSearchHandler(
		backupAssetSearchService, backupAssetSavedSearchUseService, backupAssetAuditSink,
		backupAssetHandlerConfigSource, handlers.NewBackupAssetSecretProofVerifier(dep.DB, dep.JWTManager),
	)
	backupAssetOverlayHandler := handlers.NewBackupAssetOverlayHandler(backupAssetOverlayService, backupAssetAuditSink, backupAssetHandlerConfigSource)
	backupWorkerHandler := handlers.NewBackupWorkerHandler(dep.BackupAssets).WithAudit(backupAssetAuditSink)
	backupProcessingHandler := handlers.NewBackupProcessingHandler(dep.BackupAssets, backupAssetAuditSink)
	var backupAssetExportService handlers.BackupAssetExportService
	var backupAssetExportDelivery handlers.BackupAssetExportDelivery
	var backupArchiveDelivery handlers.BackupArchiveDelivery
	var backupArchiveService handlers.BackupArchiveService
	if dep.BackupAssets != nil {
		backupAssetExportService = dep.BackupAssets.ExportService()
		backupAssetExportDelivery = dep.BackupAssets.ExportDeliveryGateway()
		backupArchiveDelivery = dep.BackupAssets.ExportDeliveryGateway()
		backupArchiveService = dep.BackupAssets.ArchiveMemberService()
	}
	backupAssetExportHandler := handlers.NewBackupAssetExportHandler(
		backupAssetExportService, backupAssetExportDelivery, dep.DB, dep.JWTManager,
		backupAssetAuditSink, backupContentConfig,
	).WithSchemePolicy(backupContentSchemePolicy)
	backupArchiveHandler := handlers.NewBackupArchiveHandler(
		backupArchiveService, backupArchiveDelivery, dep.DB, dep.JWTManager,
		backupAssetAuditSink, backupContentConfig,
	).WithSchemePolicy(backupContentSchemePolicy)
	var rsyncVersioningService handlers.TaskRsyncVersioningService = featureDisabledRsyncVersioningService{}
	if dep.BackupAssets != nil {
		rsyncVersioningService = dep.BackupAssets.RepositoryService()
	}
	rsyncVersioningHandler := handlers.NewTaskRsyncVersioningHandler(rsyncVersioningService)
	taskHandler.WithRsyncVersioningService(rsyncVersioningService)
	var rcloneVersioningService handlers.TaskRcloneVersioningService = featureDisabledRcloneVersioningService{}
	if dep.BackupAssets != nil {
		rcloneVersioningService = dep.BackupAssets.RepositoryService()
	}
	rcloneVersioningHandler := handlers.NewTaskRcloneVersioningHandler(rcloneVersioningService)
	taskHandler.WithRcloneVersioningService(rcloneVersioningService)

	v1 := router.Group("/api/v1")
	// Captcha is unauthenticated; rate-limit to reduce store spam / memory pressure.
	v1.GET("/auth/captcha", middleware.LoginRateLimitWithContext(appCtx, dep.SettingsService, dep.LoginRateLimit, dep.LoginRateWindow), captchaHandler.GenerateCaptcha)
	v1.POST("/auth/login", middleware.LoginRateLimitWithContext(appCtx, dep.SettingsService, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.Login)
	v1.POST("/auth/2fa/login", middleware.LoginRateLimitWithContext(appCtx, dep.SettingsService, dep.LoginRateLimit, dep.LoginRateWindow), authHandler.TOTPLogin)
	v1.GET("/version", versionHandler.Info)
	contentRouteHandlers := []gin.HandlerFunc{middleware.ContentSafeRecovery(), backupContentHandler.Serve}
	v1.GET("/asset-content/:deliveryId", contentRouteHandlers...)
	v1.HEAD("/asset-content/:deliveryId", contentRouteHandlers...)
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions,
		http.MethodConnect, http.MethodTrace,
	} {
		v1.Handle(method, "/asset-content/:deliveryId", contentRouteHandlers...)
	}
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace,
	} {
		v1.Handle(method, "/asset-content/:deliveryId/", contentRouteHandlers...)
	}
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
	secured.GET("/overview", middleware.RBAC("tasks:read"), overviewHandler.Get)
	secured.GET("/overview/traffic", middleware.RBAC("tasks:read"), overviewTrafficHandler.Get)
	secured.GET("/overview/health-incident-timeline", middleware.RBAC("tasks:read"), healthIncidentTimelineHandler.Get)
	secured.GET("/overview/backup-health", middleware.RBAC("tasks:read"), backupHealthHandler.Get)
	secured.GET("/overview/backup-confidence", middleware.RBAC("tasks:read"), backupConfidenceHandler.Get)
	secured.GET("/overview/storage-usage", middleware.RBAC("tasks:read"), storageUsageHandler.Get)
	secured.GET("/admin/backup-asset-processing", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(1), backupWorkerHandler.Get)
	secured.GET("/admin/backup-asset-processing/capabilities", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(1), backupWorkerHandler.Capabilities)
	secured.GET("/admin/backup-asset-processing/coverage", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(1), backupWorkerHandler.Coverage)
	secured.GET("/admin/backup-asset-processing/updater", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(1), backupWorkerHandler.Updater)
	secured.GET("/admin/backup-asset-processing/updater/offline-candidates", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(1), backupWorkerHandler.OfflineCandidates)
	secured.PATCH("/admin/backup-asset-processing/backfill-policy", middleware.RequireRole("admin"), middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(4<<10), backupWorkerHandler.UpdateBackfillPolicy)
	secured.POST("/admin/backup-asset-processing/updater/offline-candidates/scan", middleware.RequireRole("admin"), middleware.APIRateLimit(6, time.Hour), middleware.MaxBodySize(1), backupWorkerHandler.ScanOfflineCandidates)
	secured.POST("/admin/backup-asset-processing/updater/offline-imports", middleware.RequireRole("admin"), middleware.APIRateLimit(1, time.Hour), middleware.MaxBodySize(4<<10), backupWorkerHandler.ActivateOfflineCandidate)
	secured.POST("/backup-repositories/connect", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), backupRepositoryHandler.Connect)
	secured.GET("/backup-repositories", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupRepositoryHandler.List)
	secured.GET("/backup-repositories/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupRepositoryHandler.Detail)
	secured.GET("/backup-repositories/:id/recovery-points", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.ListRecoveryPoints)
	secured.GET("/backup-file-sources/nodes", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupFileSourceHandler.ListNodes)
	secured.GET("/backup-file-sources/nodes/:nodeId/sets", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupFileSourceHandler.ListBackupSets)
	secured.GET("/backup-file-sources/sets/:backupSetId/versions", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupFileSourceHandler.ListVersions)
	secured.GET("/backup-file-sources/recovery-points/:recoveryPointId/source", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupFileSourceHandler.ResolveRecoveryPointSource)
	secured.GET("/recovery-points/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.GetRecoveryPoint)
	secured.GET("/recovery-points/:id/catalog-status", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.GetCatalogStatus)
	secured.GET("/recovery-points/:id/evidence", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.GetEvidence)
	secured.GET("/recovery-points/:id/entries", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.ListEntries)
	secured.GET("/recovery-points/:id/entries/:entryId", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.GetEntry)
	secured.GET("/recovery-points/:id/entries/:entryId/versions", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.ListEntryVersions)
	secured.POST("/recovery-points/:id/entries/:entryId/delivery-tickets", middleware.RBAC(backupasset.PermissionBackupAssetsPreview), backupContentHandler.Issue)
	recoveryRouteHandlers := []gin.HandlerFunc{
		middleware.RBAC(backupasset.PermissionBackupAssetsRecover), middleware.RequireRole("admin"),
		middleware.APIRateLimit(30, time.Minute),
	}
	secured.POST("/recovery-plans", append(recoveryRouteHandlers, backupRecoveryHandler.CreatePlan)...)
	secured.GET("/recovery-plans/:id", append(recoveryRouteHandlers, backupRecoveryHandler.GetPlan)...)
	secured.POST("/recovery-plans/:id/preflights", append(recoveryRouteHandlers, backupRecoveryHandler.Preflight)...)
	secured.POST("/recovery-plans/:id/security-overrides", append(recoveryRouteHandlers, backupRecoveryHandler.SecurityOverride)...)
	secured.POST("/recovery-plans/:id/write-authorizations", append(recoveryRouteHandlers, backupRecoveryHandler.AuthorizeWrite)...)
	secured.POST("/recovery-plans/:id/execute", append(recoveryRouteHandlers, backupRecoveryHandler.Execute)...)
	secured.POST("/recovery-plans/:id/cancel", append(recoveryRouteHandlers, backupRecoveryHandler.CancelPlan)...)
	secured.GET("/recovery-jobs/:id", append(recoveryRouteHandlers, backupRecoveryHandler.GetJob)...)
	secured.GET("/recovery-jobs/:id/items", append(recoveryRouteHandlers, backupRecoveryHandler.GetJobItems)...)
	secured.GET("/recovery-jobs/:id/results", append(recoveryRouteHandlers, backupRecoveryHandler.GetJobResults)...)
	secured.POST("/recovery-jobs/:id/cancel", append(recoveryRouteHandlers, backupRecoveryHandler.CancelJob)...)
	secured.POST("/recovery-jobs/:id/exact-mirror-delete-authorizations", append(recoveryRouteHandlers, backupRecoveryHandler.AuthorizeExactMirrorDelete)...)
	secured.POST("/recovery-jobs/:id/results/:resultId/download-ticket", append(recoveryRouteHandlers, backupContentHandler.IssueRecoveryResult)...)
	secured.POST("/recovery-jobs/:id/results/retain", append(recoveryRouteHandlers, backupRecoveryHandler.RetainResults)...)
	secured.POST("/recovery-jobs/:id/results/cleanup", append(recoveryRouteHandlers, backupRecoveryHandler.CleanupResults)...)
	secured.POST("/settings/backup-assets/recovery/target-roots", append(recoveryRouteHandlers, backupRecoveryHandler.RegisterTargetRoot)...)
	secured.PUT("/settings/backup-assets/recovery/target-roots/:nodeId/:rootId", append(recoveryRouteHandlers, backupRecoveryHandler.RotateTargetRoot)...)
	secured.DELETE("/settings/backup-assets/recovery/target-roots/:nodeId/:rootId", append(recoveryRouteHandlers, backupRecoveryHandler.DeleteTargetRoot)...)
	secured.GET("/settings/backup-assets/recovery/target-roots", append(recoveryRouteHandlers, backupRecoveryHandler.ListTargetRoots)...)
	secured.POST("/settings/backup-assets/recovery/downgrade-readiness", append(recoveryRouteHandlers, backupRecoveryHandler.DowngradeReadiness)...)
	backupGAHandler := handlers.NewBackupGAHandler(dep.BackupAssets)
	gaRouteHandlers := []gin.HandlerFunc{
		middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"),
	}
	secured.POST("/settings/backup-assets/ga/inventory", append(gaRouteHandlers, backupGAHandler.Inventory)...)
	secured.GET("/settings/backup-assets/ga/readiness", append(gaRouteHandlers, backupGAHandler.Readiness)...)
	secured.POST("/settings/backup-assets/ga/acknowledge", append(gaRouteHandlers, backupGAHandler.Acknowledge)...)
	secured.POST("/recovery-points/:id/entries/:entryId/preview-jobs",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(30, time.Minute),
		middleware.MaxBodySize(4<<10), backupProcessingHandler.CreatePreview)
	secured.GET("/recovery-points/:id/entries/:entryId/preview-jobs/:jobId",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(60, time.Minute),
		middleware.MaxBodySize(1), backupProcessingHandler.PollPreview)
	secured.POST("/recovery-points/:id/entries/:entryId/preview-jobs/:jobId/cancel",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(30, time.Minute),
		middleware.MaxBodySize(4<<10), backupProcessingHandler.CancelPreview)
	secured.GET("/recovery-points/:id/entries/:entryId/processing",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(60, time.Minute),
		middleware.MaxBodySize(1), backupProcessingHandler.GetState)
	secured.POST("/asset-exports",
		middleware.RBAC(backupasset.PermissionBackupAssetsExport), middleware.RequireRole("admin"),
		middleware.APIRateLimit(10, time.Minute), middleware.MaxBodySize(2<<20), backupAssetExportHandler.Create)
	secured.GET("/asset-exports/:id",
		middleware.RBAC(backupasset.PermissionBackupAssetsExport), middleware.RequireRole("admin"),
		middleware.APIRateLimit(60, time.Minute), middleware.MaxBodySize(1), backupAssetExportHandler.Status)
	secured.POST("/asset-exports/:id/cancel",
		middleware.RBAC(backupasset.PermissionBackupAssetsExport), middleware.RequireRole("admin"),
		middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(4<<10), backupAssetExportHandler.Cancel)
	secured.POST("/asset-exports/:id/download-ticket",
		middleware.RBAC(backupasset.PermissionBackupAssetsExport), middleware.RequireRole("admin"),
		middleware.APIRateLimit(30, time.Minute), middleware.MaxBodySize(4<<10), backupAssetExportHandler.DownloadTicket)
	archiveMemberBase := "/recovery-points/:id/entries/:entryId"
	secured.GET(archiveMemberBase+"/archive-members",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(60, time.Minute),
		middleware.MaxBodySize(1), backupArchiveHandler.List)
	secured.POST(archiveMemberBase+"/archive-member-jobs",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(30, time.Minute),
		middleware.MaxBodySize(4<<10), backupArchiveHandler.Create)
	secured.GET(archiveMemberBase+"/archive-member-jobs/:jobId",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(60, time.Minute),
		middleware.MaxBodySize(1), backupArchiveHandler.Status)
	secured.POST(archiveMemberBase+"/archive-member-jobs/:jobId/cancel",
		middleware.RBAC(backupasset.PermissionBackupAssetsPreview), middleware.APIRateLimit(30, time.Minute),
		middleware.MaxBodySize(4<<10), backupArchiveHandler.Cancel)
	secured.POST(archiveMemberBase+"/archive-member-jobs/:jobId/delivery-ticket",
		middleware.RBAC(backupasset.PermissionBackupAssetsDownload), middleware.APIRateLimit(30, time.Minute),
		middleware.MaxBodySize(4<<10), backupArchiveHandler.DeliveryTicket)
	secured.POST("/recovery-point-diffs", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetHandler.Diff)
	secured.POST("/asset-search", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetSearchHandler.Search)
	secured.GET("/asset-saved-searches", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.ListSavedSearches)
	secured.POST("/asset-saved-searches", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.CreateSavedSearch)
	secured.GET("/asset-saved-searches/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.GetSavedSearch)
	secured.PATCH("/asset-saved-searches/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.UpdateSavedSearch)
	secured.DELETE("/asset-saved-searches/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.DeleteSavedSearch)
	secured.GET("/asset-favorites", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.ListFavorites)
	secured.POST("/asset-favorites", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.AddFavorite)
	secured.DELETE("/asset-favorites/:recoveryPointId/:entryId", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.RemoveFavorite)
	secured.GET("/asset-tags", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.ListTags)
	secured.POST("/asset-tags", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.CreateTag)
	secured.PATCH("/asset-tags/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.UpdateTag)
	secured.DELETE("/asset-tags/:id", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.DeleteTag)
	secured.POST("/asset-tags/:id/assignments", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.AssignTag)
	secured.DELETE("/asset-tags/:id/assignments/:recoveryPointId/:entryId", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.UnassignTag)
	secured.GET("/asset-recent", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.ListRecent)
	secured.POST("/asset-recent/clear", middleware.RBAC(backupasset.PermissionBackupAssetsList), backupAssetOverlayHandler.ClearRecent)
	secured.POST("/backup-repositories/:id/reconcile", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), backupRepositoryHandler.Reconcile)
	secured.POST("/backup-repositories/:id/disconnect", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), backupRepositoryHandler.Disconnect)
	lifecycleManage := []gin.HandlerFunc{
		middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"),
	}
	lifecyclePurge := []gin.HandlerFunc{
		middleware.RBAC(backupasset.PermissionBackupRepositoriesPurge), middleware.RequireRole("admin"),
	}
	secured.POST("/backup-repositories/:id/import-scans", append(lifecycleManage, backupRepositoryHandler.ImportScan)...)
	secured.GET("/backup-repositories/:id/import-candidates", append(lifecycleManage, backupRepositoryHandler.ListImportCandidates)...)
	secured.POST("/backup-repositories/:id/import-candidates/:candidateId/reviews", append(lifecycleManage, backupRepositoryHandler.ReviewImportCandidate)...)
	secured.POST("/backup-repositories/:id/rebuilds", append(lifecycleManage, backupRepositoryHandler.Rebuild)...)
	secured.GET("/backup-retention-policies", append(lifecycleManage, backupRetentionHandler.ListPolicies)...)
	secured.POST("/backup-retention-policies", append(lifecycleManage, backupRetentionHandler.CreatePolicy)...)
	secured.PATCH("/backup-retention-policies/:id", append(lifecycleManage, backupRetentionHandler.UpdatePolicy)...)
	secured.DELETE("/backup-retention-policies/:id", append(lifecycleManage, backupRetentionHandler.DeletePolicy)...)
	secured.POST("/backup-retention-policies/:id/impact", append(lifecycleManage, backupRetentionHandler.PreviewImpact)...)
	secured.GET("/recovery-points/:id/holds", append(lifecycleManage, backupRetentionHandler.ListHolds)...)
	secured.POST("/recovery-points/:id/holds", append(lifecycleManage, backupRetentionHandler.CreateHold)...)
	secured.POST("/recovery-points/:id/holds/:holdId/release", append(lifecycleManage,
		handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionRetentionHoldRelease, "retention_hold_release", "hold_release"),
		backupRetentionHandler.ReleaseHold,
	)...)
	secured.POST("/backup-repositories/:id/purge-preview", append(lifecyclePurge, backupRetentionHandler.PreviewPurge)...)
	secured.POST("/backup-repositories/:id/purge-plans", append(lifecyclePurge, backupRetentionHandler.CreatePurgePlan)...)
	secured.POST("/backup-repositories/:id/purges", append(lifecyclePurge,
		handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionRepositoryPurge, "repository_purge", "repository_purge"),
		backupRetentionHandler.ExecutePurge,
	)...)
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
	secured.GET("/ssh-keys/export", middleware.RBAC("ssh_keys:read"), handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionSSHKeyExport, sshutil.PurposeSSHKeyExport, "ssh_export"), sshKeyHandler.Export)
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
	secured.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(dep.DB), handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionTaskManualTrigger, sshutil.PurposeTaskCommand, "task_run"), handlers.RequireTaskManualTriggerCredentialGrant(dep.DB), taskHandler.Trigger)
	secured.POST("/tasks/:id/cancel", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Cancel)
	secured.POST("/tasks/:id/pause", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Pause)
	secured.POST("/tasks/:id/resume", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Resume)
	secured.POST("/tasks/:id/skip-next", middleware.RBAC("tasks:write"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.SkipNext)
	secured.POST("/tasks/:id/restore", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionTaskRestoreTrigger, sshutil.PurposeTaskRestore, "task_restore"), handlers.RequireTaskRestoreCredentialGrant(dep.DB), taskHandler.Restore)
	secured.GET("/tasks/:id/backup-files", middleware.RequireRole("admin"), fileHandler.ListTaskBackupFiles)
	secured.POST("/tasks/:id/rsync-versioning/preflights", middleware.RBAC("tasks:write"), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rsyncVersioningHandler.CreatePreflight)
	secured.POST("/tasks/:id/rsync-versioning/activate", middleware.RBAC("tasks:write"), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rsyncVersioningHandler.Activate)
	secured.POST("/tasks/:id/rsync-versioning/rollback-preparations", middleware.RBAC("tasks:write"), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rsyncVersioningHandler.PrepareRollback)
	secured.POST("/tasks/:id/rclone-versioning/portable-binding-setups", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.CreatePortableBindingSetup)
	secured.PUT("/tasks/:id/rclone-versioning/portable-binding", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.SetPortableBinding)
	secured.POST("/tasks/:id/rclone-versioning/native-binding-setups", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.CreateNativeBindingSetup)
	secured.PUT("/tasks/:id/rclone-versioning/native-binding", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.SetNativeBinding)
	secured.POST("/tasks/:id/rclone-versioning/preflights", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.CreatePreflight)
	secured.POST("/tasks/:id/rclone-versioning/activate", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.Activate)
	secured.POST("/tasks/:id/rclone-versioning/clean-rollbacks", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.CleanRollback)
	secured.POST("/tasks/:id/rclone-versioning/rollback-preparations", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(dep.DB), rcloneVersioningHandler.PrepareRollback)

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

	secured.GET("/tasks/:id/snapshots", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotHandler.ListSnapshots)
	secured.GET("/tasks/:id/snapshots/:sid/files", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotHandler.ListFiles)
	secured.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionSnapshotRestore, sshutil.PurposeSnapshot, "snapshot_restore"), handlers.RequireSnapshotRestoreCredentialGrant(dep.DB), snapshotHandler.Restore)
	secured.GET("/tasks/:id/snapshots/diff", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotDiffHandler.Diff)
	secured.GET("/tasks/:id/snapshots/search", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), snapshotSearchHandler.Search)

	secured.GET("/settings", middleware.RequireRole("admin"), settingsHandler.GetAll)
	secured.GET("/settings/security-risk-summary", middleware.RequireRole("admin"), settingsHandler.SecurityRiskSummary)
	secured.PUT("/settings", middleware.RequireRole("admin"), settingsHandler.BatchUpdate)
	secured.DELETE("/settings/:key", middleware.RequireRole("admin"), settingsHandler.Delete)

	secured.GET("/config/export", middleware.RequireRole("admin"), handlers.RequireStepUpIf(dep.DB, dep.JWTManager, auth.StepUpActionConfigExport, handlers.CredentialGrantPurposeConfigExport, "settings_export_sensitive", func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), handlers.RequireConfigExportCredentialGrantIf(dep.DB, func(c *gin.Context) bool {
		return c.Query("include_secrets") == "true"
	}), configHandler.Export)
	secured.POST("/config/import", middleware.RequireRole("admin"), handlers.RequireStepUp(dep.DB, dep.JWTManager, auth.StepUpActionConfigImport, handlers.CredentialGrantPurposeConfigImport, "settings_import"), handlers.RequireConfigImportCredentialGrant(dep.DB), configHandler.Import)

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
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	router.GET("/readyz", func(c *gin.Context) {
		if dep.DB == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database unavailable"})
			return
		}
		sqlDB, err := dep.DB.DB()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "database unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
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

	// Swagger UI: disabled in production unless SWAGGER_ENABLED=true.
	// Exposes full API surface without auth — never enable casually in prod.
	if swaggerUIEnabled() {
		router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}
	router.NoRoute(func(c *gin.Context) {
		if middleware.IsBackupContentShapedPath(c.Request.URL.Path) {
			backupContentHandler.Reject(c)
			return
		}
		c.String(http.StatusNotFound, "404 page not found")
	})

	return router
}

func swaggerUIEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("SWAGGER_ENABLED"))
	if raw != "" {
		v, err := strconv.ParseBool(raw)
		if err == nil {
			return v
		}
	}
	// Default: on outside production, off in production.
	return !util.IsProductionEnv()
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
