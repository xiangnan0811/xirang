package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/anomaly"
	"xirang/backend/internal/api"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/dashboards/providers"
	"xirang/backend/internal/database"
	"xirang/backend/internal/escalation"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/metrics"
	"xirang/backend/internal/model"
	"xirang/backend/internal/nodelogs"
	"xirang/backend/internal/probe"
	"xirang/backend/internal/reporting"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/slo"
	"xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
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

	// 自动将 v1（SHA-256 KDF）加密数据迁移到 v2（Argon2id KDF）
	if bootstrap.HasV1EncryptedData(db) {
		if err := bootstrap.MigrateEncryptionV1ToV2(db); err != nil {
			log.Error().Err(err).Msg("加密数据迁移失败，v1 数据仍可正常解密")
		}
	}

	hub := ws.NewHub(db, cfg.AllowedOrigins, cfg.WSAllowEmptyOrigin)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	cronScheduler := scheduler.NewCronScheduler()
	cronScheduler.Start()
	defer cronScheduler.Stop()

	settingsSvc := settings.NewService(db)
	alerting.InitSettings(settingsSvc)

	escSvc := escalation.NewService(db)

	// Inject resolver into alerting so RaiseXxx functions know whether to defer to engine
	alerting.InitEscalationResolver(func(alert model.Alert) (*alerting.EscalationPolicySummary, error) {
		policy, err := escSvc.ResolvePolicyForAlert(hubCtx, alert)
		if err != nil {
			return nil, err
		}
		if policy == nil {
			return nil, nil
		}
		return &alerting.EscalationPolicySummary{Enabled: policy.Enabled, MinSeverity: policy.MinSeverity}, nil
	})

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
				_ = db.First(&node, alert.NodeID).Error
			}
			return alerting.MatchSilence(alert, node, sils, time.Now())
		},
		// sender — dispatch to the level's integrations
		func(alert model.Alert, ids []uint) {
			alerting.DispatchToIntegrations(db, alert, ids)
		},
	)
	go escEngine.Run(hubCtx)

	// Anomaly detection engine + retention
	anomalyEngine := anomaly.NewEngine(
		db, settingsSvc,
		anomaly.NewEWMADetector(db, settingsSvc),
		anomaly.NewDiskForecastDetector(db, settingsSvc),
	)
	anomalyEngine.SetRaiseFn(anomaly.NewRaiseFn(db, func(rdb *gorm.DB, nodeID uint, severity, errorCode, message string) (uint, bool, error) {
		return alerting.RaiseAnomalyAlert(rdb, alerting.AnomalyAlertInput{
			NodeID: nodeID, Severity: severity, ErrorCode: errorCode, Message: message,
		})
	}))
	go anomalyEngine.Run(hubCtx)

	anomalyRetention := anomaly.NewRetentionWorker(db, settingsSvc)
	go anomalyRetention.Run(hubCtx)

	executorFactory := executor.NewFactory(cfg.RsyncBinary)
	taskManager := task.NewManager(db, executorFactory, hub, cronScheduler, settingsSvc, cfg.TaskTrafficRetentionDays, cfg.TaskRunRetentionDays)
	if err := taskManager.LoadSchedules(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("加载定时任务失败")
	}

	metricSink := metrics.NewFanSink(metrics.NewDBSink(db))
	prober := probe.NewProber(db, cfg.NodeProbeInterval, cfg.NodeProbeFailThreshold, cfg.NodeProbeConcurrency, metricSink)
	prober.Start(hubCtx)

	aggregator := metrics.NewAggregator(db, cfg.DBType)
	if err := aggregator.Start(hubCtx); err != nil {
		log.Error().Err(err).Msg("启动指标聚合器失败")
		// Non-fatal: continue without aggregator. Raw samples still accumulate;
		// the hourly/daily tiers just fall behind until the next restart.
	}

	reportScheduler := reporting.NewScheduler(hubCtx, db)
	reportScheduler.Start()

	retryWorker := alerting.NewRetryWorker(db)
	go retryWorker.Run(hubCtx)

	silenceRetention := alerting.NewSilenceRetentionWorker(db, settingsSvc)
	go silenceRetention.Run(hubCtx)

	sloEvaluator := slo.NewEvaluator(db)
	sloEvaluator.SetRaiseFn(func(_ any, def *model.SLODefinition, c *slo.Compliance) error {
		return alerting.RaiseSLOBreach(db, def, c)
	})
	go sloEvaluator.Run(hubCtx)

	nodelogs.InitSettings(settingsSvc)
	nodeLogRunner := nodelogs.NewSSHRunner(db)
	nodeLogScheduler := nodelogs.NewScheduler(db, nodeLogRunner)
	go nodeLogScheduler.Run(hubCtx)

	nodeLogRetention := nodelogs.NewRetentionWorker(db)
	go nodeLogRetention.Run(hubCtx)

	dashboards.Register(providers.NewNodeProvider(db))
	dashboards.Register(providers.NewTaskProvider(db))

	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)
	jwtManager.SetDB(db)
	authService := auth.NewService(db, jwtManager, settingsSvc, auth.LoginSecurityConfig{
		FailLockThreshold: cfg.LoginFailLockThreshold,
		FailLockDuration:  cfg.LoginFailLockDuration,
	})

	router := api.NewRouter(api.Dependencies{
		AppContext:                hubCtx,
		DB:                        db,
		AuthService:               authService,
		JWTManager:                jwtManager,
		TaskManager:               taskManager,
		Hub:                       hub,
		SettingsService:           settingsSvc,
		AllowedOrigins:            cfg.AllowedOrigins,
		LoginRateLimit:            cfg.LoginRateLimit,
		LoginRateWindow:           cfg.LoginRateWindow,
		LoginCaptchaEnabled:       cfg.LoginCaptchaEnabled,
		LoginSecondCaptchaEnabled: cfg.LoginSecondCaptchaEnabled,
		RetryWorker:               retryWorker,
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

	taskManager.StopAccepting()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("优雅关闭失败，强制退出")
	}

	if err := taskManager.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("任务管理器关闭失败")
	}
	if err := prober.Stop(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("节点探测停止失败")
	}
	if err := aggregator.Stop(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("指标聚合器停止失败")
	}
	hubCancel()
}
