package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xirang/backend/internal/alerting"
	"xirang/backend/internal/api"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/probe"
	"xirang/backend/internal/reporting"
	"xirang/backend/internal/settings"
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

	executorFactory := executor.NewFactory(cfg.RsyncBinary)
	taskManager := task.NewManager(db, executorFactory, hub, cronScheduler, settingsSvc, cfg.TaskTrafficRetentionDays, cfg.TaskRunRetentionDays)
	if err := taskManager.LoadSchedules(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("加载定时任务失败")
	}

	prober := probe.NewProber(db, cfg.NodeProbeInterval, cfg.NodeProbeFailThreshold, cfg.NodeProbeConcurrency)
	prober.Start(hubCtx)

	reportScheduler := reporting.NewScheduler(hubCtx, db)
	reportScheduler.Start()

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
	hubCancel()
}
