package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gorm.io/gorm"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/anomaly"
	"xirang/backend/internal/api"
	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/automation"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/provider"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/dashboards/providers"
	"xirang/backend/internal/database"
	"xirang/backend/internal/escalation"
	"xirang/backend/internal/lifecycle"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/metrics"
	"xirang/backend/internal/model"
	"xirang/backend/internal/nodelogs"
	"xirang/backend/internal/probe"
	"xirang/backend/internal/reporting"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/slo"
	"xirang/backend/internal/snapshot"
	"xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/uptime"
	"xirang/backend/internal/util"
	"xirang/backend/internal/version"
	"xirang/backend/internal/ws"
)

// @title           Xirang API
// @version         1.0
// @description     息壤 — 服务器运维管理平台 API
// @host            localhost:8080
// @BasePath        /api/v1
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description JWT Bearer token (格式: Bearer <token>)
func main() {
	logger.Init(os.Getenv("LOG_LEVEL"))
	log := logger.Module("main")
	log.Info().
		Str("version", version.Version).
		Str("commit", version.GitCommit).
		Str("built", version.BuildTime).
		Msg("Xirang 启动")

	// Two-tier configuration model:
	//   - config.Config (below): startup-critical env vars, immutable after Load().
	//     These are the "hard" boot defaults (DB, JWT, listen addr, SSH policy).
	//   - settings.Service: runtime-configurable DB-backed values, mutable via API.
	//     Resolution: DB value > env var > code default.
	//     See internal/settings/service.go for the full registry and precedence.
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("加载配置失败")
	}

	db, err := database.Open(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("连接数据库失败")
	}

	if err := bootstrap.AutoMigrate(db, cfg.DBType); err != nil {
		log.Fatal().Err(err).Msg("执行数据库迁移失败")
	}
	if err := bootstrap.SeedUsers(db); err != nil {
		log.Fatal().Err(err).Msg("初始化管理员账号失败")
	}
	bootstrap.SeedPolicyTemplates(db)

	if err := alerting.ValidateConfig(); err != nil {
		log.Fatal().Err(err).Msg("alerting 配置校验失败")
	}

	// 自动将 v1（SHA-256 KDF）加密数据迁移到 v2（Argon2id KDF）
	if bootstrap.HasV1EncryptedData(db) {
		if err := bootstrap.MigrateEncryptionV1ToV2(db); err != nil {
			log.Error().Err(err).Msg("加密数据迁移失败，v1 数据仍可正常解密")
		}
	}
	// Drill verify scripts were plain at rest before model hooks; always encrypt
	// residual plaintext (idempotent). Failure must abort startup — otherwise
	// scripts may remain readable at rest with no health signal.
	if err := bootstrap.EncryptPlaintextPolicyDrillScripts(db); err != nil {
		log.Fatal().Err(err).Msg("策略演练脚本明文加密失败，拒绝启动")
	}

	hub := ws.NewHub(db, cfg.AllowedOrigins, cfg.WSAllowEmptyOrigin)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	cronScheduler := scheduler.NewCronScheduler()
	cronScheduler.Start()
	defer cronScheduler.Stop()

	settingsSvc := settings.NewService(db)
	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)
	jwtManager.SetDB(db)
	assetRuntime, err := backupruntime.New(backupruntime.Dependencies{
		DB: db, Settings: settingsSvc, SessionRevocations: jwtManager,
		ToolBinaries: provider.ToolBinaries{
			Restic: util.GetEnvOrDefault("RESTIC_BINARY", "restic"),
			Rclone: util.GetEnvOrDefault("RCLONE_BINARY", "rclone"),
		},
	})
	if err != nil {
		log.Fatal().Err(err).Msg("构建备份资产运行时失败")
	}
	raiser := alerting.DefaultRaiser{DB: db}

	// 自动化规则引擎 —— 在任务/异常事件发生时匹配规则并执行动作
	autoDispatcher := automation.NewDispatcher(db)

	escSvc := escalation.NewService(db)

	// alertDispatcher is the canonical Dispatcher — it bundles db, settings, and
	// escalation resolver so that RaiseXxx calls no longer need to reach for
	// package-level module vars. Callers receive it via constructors.
	alertDispatcher := alerting.NewDispatcher(db, settingsSvc, func(alert model.Alert) (*alerting.EscalationPolicySummary, error) {
		policy, err := escSvc.ResolvePolicyForAlert(hubCtx, alert)
		if err != nil {
			return nil, err
		}
		if policy == nil {
			return nil, nil
		}
		return &alerting.EscalationPolicySummary{Enabled: policy.Enabled, MinSeverity: policy.MinSeverity}, nil
	})
	alerting.SetDispatcher(alertDispatcher)

	// Engine
	escEngine := escalation.NewEngine(
		db, escSvc,
		// Silence check is gated by the delay-elapsed guard in engine.evaluate, so
		// ActiveSilences is only queried when a level is actually ready to fire, not
		// per tick per alert. The N+1 is therefore bounded to firing events only.
		func(alert model.Alert) *model.Silence {
			sils, err := alerting.ActiveSilences(db, time.Now())
			if err != nil {
				return nil
			}
			var node model.Node
			if alert.NodeID > 0 {
				if err := db.First(&node, alert.NodeID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					logger.Module("alerting").Warn().Err(err).Uint("node_id", alert.NodeID).Msg("静默检查时查询节点失败")
				}
			}
			return alerting.MatchSilence(alert, node, sils, time.Now())
		},
		// dispatcher - fires the level's integration list via DefaultRaiser
		raiser,
	)

	// Anomaly detection engine + retention
	anomalySink := anomaly.NewSink(db, settingsSvc, func(_ *gorm.DB, nodeID uint, severity, errorCode, message string) (uint, bool, error) {
		return raiser.RaiseAnomalyAlert(alerting.AnomalyAlertInput{
			NodeID: nodeID, Severity: severity, ErrorCode: errorCode, Message: message,
		})
	}, autoDispatcher)
	anomalyEngine := anomaly.NewEngine(
		db, settingsSvc,
		anomalySink,
		anomaly.NewEWMADetector(db, settingsSvc),
		anomaly.NewDiskForecastDetector(db, settingsSvc),
	)

	anomalyRetention := anomaly.NewRetentionWorker(db, settingsSvc)

	executorFactory := executor.NewFactoryWithPublicationStrategies(
		cfg.RsyncBinary,
		assetRuntime.ResticPublicationStrategy(),
		assetRuntime.RsyncTreePublicationStrategy(),
		assetRuntime.RclonePublicationStrategy(),
	)
	legacyRestic, ok := executorFactory.Resolve("restic").(*executor.ResticExecutor)
	if !ok {
		log.Fatal().Msg("Restic legacy adapter type mismatch")
	}
	taskManager := task.NewManager(db, executorFactory, hub, cronScheduler, settingsSvc, alertDispatcher, cfg.TaskTrafficRetentionDays, cfg.TaskRunRetentionDays)
	taskManager.SetPublicationCoordinator(assetRuntime.PublicationCoordinator())
	taskManager.SetLineageGuard(assetRuntime.LineageGuard())
	taskManager.SetLegacyBlockRecorder(assetRuntime.LegacyBlockRecorder())
	taskManager.SetAnomalySink(anomalySink)
	taskManager.SetExactAnomalyAnalyzer(func(ctx context.Context, taskEntity model.Task, runID uint, currentID, previousID string) ([]anomaly.Finding, error) {
		return anomaly.AnalyzeSnapshotDiffExact(ctx, db, taskEntity, runID, currentID, previousID, assetRuntime.LineageGuard(), legacyRestic)
	})
	taskManager.SetAutomationDispatcher(autoDispatcher)
	autoDispatcher.SetTaskTriggerer(taskManager)
	if err := assetRuntime.SetCommitObserver(taskManager); err != nil {
		log.Fatal().Err(err).Msg("配置备份资产提交观察器失败")
	}
	if err := assetRuntime.SetInterruptedRunReporter(taskManager); err != nil {
		log.Fatal().Err(err).Msg("配置备份资产中断对账失败")
	}
	if err := assetRuntime.SetInterruptedRunReadiness(taskManager); err != nil {
		log.Fatal().Err(err).Msg("配置备份资产中断就绪检查失败")
	}
	if err := assetRuntime.StartupPass(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("备份资产启动对账失败")
	}
	workerServers, workerServerErr := startWorkerHTTPServers(assetRuntime)
	if workerServerErr != nil {
		log.Warn().Str("stage", "worker_listener_startup").Msg("备份资产 Worker 监听器未完全启动，核心服务继续运行")
	}
	if err := taskManager.LoadSchedules(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("加载定时任务失败")
	}
	taskManager.StartDrillLoop()

	taskRetention := task.NewRetentionWorker(settingsSvc, taskManager)

	sinks := []metrics.Sink{metrics.NewDBSink(db)}
	if rs := buildRemoteWriteSinkFromConfig(settingsSvc); rs != nil {
		sinks = append(sinks, rs)
	}
	metricSink := metrics.NewFanSink(sinks...)
	prober := probe.NewProber(db, cfg.NodeProbeInterval, cfg.NodeProbeFailThreshold, cfg.NodeProbeConcurrency, metricSink, alertDispatcher)

	uptimeProber := uptime.NewProber(db, 60*time.Second)
	uptimeProber.SetAlertCallback(func(monitor model.ServiceMonitor, oldStatus, newStatus string) {
		if newStatus == "down" {
			_, _, alertErr := raiser.RaiseAnomalyAlert(alerting.AnomalyAlertInput{
				NodeID:    0, // service monitors are not node-scoped
				NodeName:  monitor.Name,
				Severity:  "critical",
				ErrorCode: fmt.Sprintf("XR-SERVICE-DOWN-%d", monitor.ID),
				Message:   fmt.Sprintf("服务 %s 不可达 (%s)", monitor.Name, monitor.Target),
			})
			if alertErr != nil {
				logger.Module("uptime").Warn().Uint("monitor_id", monitor.ID).Err(alertErr).Msg("创建 down 告警失败")
			}
		} else if newStatus == "up" && oldStatus == "down" {
			if resolveErr := db.Model(&model.Alert{}).Where("error_code = ? AND status = 'open'",
				fmt.Sprintf("XR-SERVICE-DOWN-%d", monitor.ID)).
				Updates(map[string]interface{}{
					"status":     "resolved",
					"updated_at": time.Now(),
				}).Error; resolveErr != nil {
				logger.Module("uptime").Warn().Uint("monitor_id", monitor.ID).Err(resolveErr).Msg("恢复 down 告警失败")
			}
		}
	})

	aggregator := metrics.NewAggregator(db, cfg.DBType)

	reportScheduler := reporting.NewScheduler(db)

	retryWorker := alerting.NewRetryWorker(db)

	silenceRetention := alerting.NewSilenceRetentionWorker(db, settingsSvc)

	sloEvaluator := slo.NewEvaluator(db, raiser)

	nodeLogRunner := nodelogs.NewSSHRunner(db)
	nodeLogScheduler := nodelogs.NewScheduler(db, nodeLogRunner)

	nodeLogRetention := nodelogs.NewRetentionWorker(db, settingsSvc)

	// LIFECYCLE PHASE: assemble workers in startup order, then start all.
	workers := []lifecycle.Worker{
		prober,
		uptimeProber,
		aggregator,
		assetRuntime,
		taskManager,
		taskRetention,
		reportScheduler,
		retryWorker,
		silenceRetention,
		sloEvaluator,
		nodeLogScheduler,
		nodeLogRetention,
		anomalyEngine,
		anomalyRetention,
		escEngine,
	}
	for _, w := range workers {
		go w.Run(hubCtx)
	}

	dashboards.Register(providers.NewNodeProvider(db))
	dashboards.Register(providers.NewTaskProvider(db))

	authService := auth.NewService(db, jwtManager, settingsSvc, auth.LoginSecurityConfig{
		FailLockThreshold:       cfg.LoginFailLockThreshold,
		FailLockDuration:        cfg.LoginFailLockDuration,
		GlobalFailLockThreshold: cfg.LoginGlobalFailLockThreshold,
		GlobalFailLockDuration:  cfg.LoginGlobalFailLockDuration,
	})
	snapshotIndexer := snapshot.NewIndexer(db, assetRuntime.LineageGuard(), assetRuntime.FoundationService())

	router := api.NewRouter(api.Dependencies{
		AppContext:        hubCtx,
		DB:                db,
		AuthService:       authService,
		JWTManager:        jwtManager,
		TaskManager:       taskManager,
		Hub:               hub,
		SettingsService:   settingsSvc,
		AllowedOrigins:    cfg.AllowedOrigins,
		LoginRateLimit:    cfg.LoginRateLimit,
		LoginRateWindow:   cfg.LoginRateWindow,
		RetryWorker:       retryWorker,
		AlertDispatcher:   alertDispatcher,
		MetricsToken:      cfg.MetricsToken,
		MetricsRateLimit:  cfg.MetricsRateLimit,
		TrustedProxies:    cfg.TrustedProxies,
		MetricsRateWindow: cfg.MetricsRateWindow,
		BackupAssets:      assetRuntime,
		BackupContent:     assetRuntime.ContentBroker(),
		BackupContentConfig: func(context.Context) (handlers.BackupContentHandlerConfig, error) {
			contentConfig, contentConfigErr := assetRuntime.ContentConfig()
			if contentConfigErr != nil {
				return handlers.BackupContentHandlerConfig{}, contentConfigErr
			}
			return handlers.BackupContentHandlerConfig{
				TicketTimeout: contentConfig.TicketTimeout, AllowInsecureLoopback: contentConfig.AllowInsecureLoopback,
			}, nil
		},
		LegacyResticSnapshots: legacyRestic,
		SnapshotDiffRunner:    legacyRestic,
		SnapshotIndexer:       snapshotIndexer,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("后端服务启动")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("服务异常退出")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("收到退出信号，开始优雅关闭")

	cronScheduler.Stop()
	taskManager.StopAccepting()
	if err := workerServers.StopAccepting(); err != nil {
		log.Warn().Str("stage", "worker_listener_close").Msg("备份资产 Worker 监听器关闭不完整")
	}
	assetRuntime.StopAccepting()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := workerServers.Shutdown(shutdownCtx); err != nil {
		log.Warn().Str("stage", "worker_http_shutdown").Msg("备份资产 Worker 请求未在宽限期内完成")
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("优雅关闭失败，强制退出")
	}

	// LIFO drain: workers started last finish first to invert the dependency
	// stack. Errors are logged but never abort -- we want every worker to
	// receive a shutdown signal even if one fails.
	for i := len(workers) - 1; i >= 0; i-- {
		if err := workers[i].Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Int("index", i).Str("worker", fmt.Sprintf("%T", workers[i])).Msg("shutdown worker failed")
		}
	}
	// All task/provider workers have joined, so no command can still need a
	// temporary SSH key file. Cleanup remains after the bounded runtime drain.
	executor.CleanupTempKeyDir()
	hubCancel()
}

// buildRemoteWriteSinkFromConfig reads METRICS_REMOTE_URL / _BEARER_TOKEN /
// _TIMEOUT env vars first, falling back to settings.GetEffective. Returns
// nil when no URL is configured (sink disabled). Read once at boot;
// changes require restart.
func buildRemoteWriteSinkFromConfig(svc *settings.Service) *metrics.RemoteWriteSink {
	url := strings.TrimSpace(os.Getenv("METRICS_REMOTE_URL"))
	if url == "" && svc != nil {
		url = strings.TrimSpace(svc.GetEffective("metrics.remote_url"))
	}
	if url == "" {
		return nil
	}
	token := strings.TrimSpace(os.Getenv("METRICS_REMOTE_BEARER_TOKEN"))
	if token == "" && svc != nil {
		token = strings.TrimSpace(svc.GetEffective("metrics.remote_bearer_token"))
	}
	timeout := 5 * time.Second
	if raw := strings.TrimSpace(os.Getenv("METRICS_REMOTE_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	return metrics.NewRemoteWriteSink(url, token, timeout)
}

type workerHTTPServerSet struct {
	mu        sync.Mutex
	servers   []*http.Server
	listeners []net.Listener
}

func startWorkerHTTPServers(runtime *backupruntime.Runtime) (*workerHTTPServerSet, error) {
	servers := &workerHTTPServerSet{}
	if runtime == nil {
		return servers, nil
	}
	config, err := runtime.ProcessingConfig()
	if err != nil {
		return servers, err
	}
	if !config.Enabled || (!config.LocalWorker.Enabled && !config.RemoteWorker.Enabled) {
		return servers, nil
	}
	protocol := runtime.WorkerProtocol()
	if protocol == nil {
		return servers, nil
	}
	handler, err := api.NewWorkerRouter(protocol, api.WorkerRouterConfig{
		JSONMaxBytes: config.ProtocolJSONMaxBytes, ArtifactMaxBytes: config.Sink.ArtifactMaxBytes,
	})
	if err != nil {
		return servers, err
	}
	var startupErrors []error
	if config.LocalWorker.Enabled {
		listener, listenErr := processing.ListenLocalWorker(processing.LocalTransportConfig{SocketPath: config.LocalWorker.Socket})
		if listenErr != nil {
			startupErrors = append(startupErrors, listenErr)
		} else {
			servers.serve("local", listener, handler)
		}
	}
	if config.RemoteWorker.Enabled {
		listener, listenErr := processing.ListenRemoteWorker(processing.RemoteTransportConfig{
			Enabled: true, ListenAddress: config.RemoteWorker.ListenAddress,
			ServerCertFile: config.RemoteWorker.ServerCertFile, ServerKeyFile: config.RemoteWorker.ServerKeyFile,
			ClientCAFile: config.RemoteWorker.ClientCAFile, TrustDomain: config.RemoteWorker.TrustDomain,
		})
		if listenErr != nil {
			startupErrors = append(startupErrors, listenErr)
		} else {
			servers.serve("mtls", listener, handler)
		}
	}
	return servers, errors.Join(startupErrors...)
}

func newWorkerHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ConnContext:       api.WorkerConnContext,
		ErrorLog:          stdlog.New(io.Discard, "", 0),
	}
}

func (servers *workerHTTPServerSet) serve(kind string, listener net.Listener, handler http.Handler) {
	server := newWorkerHTTPServer(handler)
	servers.mu.Lock()
	servers.servers = append(servers.servers, server)
	servers.listeners = append(servers.listeners, listener)
	servers.mu.Unlock()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			logger.Module("backup_asset_processing").Warn().Str("transport", kind).Str("stage", "serve").Msg("备份资产 Worker 监听器停止")
		}
	}()
}

func (servers *workerHTTPServerSet) StopAccepting() error {
	if servers == nil {
		return nil
	}
	servers.mu.Lock()
	listeners := append([]net.Listener(nil), servers.listeners...)
	servers.listeners = nil
	servers.mu.Unlock()
	var closeErrors []error
	for _, listener := range listeners {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func (servers *workerHTTPServerSet) Shutdown(ctx context.Context) error {
	if servers == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	servers.mu.Lock()
	values := append([]*http.Server(nil), servers.servers...)
	servers.mu.Unlock()
	var shutdownErrors []error
	for _, server := range values {
		if err := server.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	return errors.Join(shutdownErrors...)
}
