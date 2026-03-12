package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xirang/backend/internal/api"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/probe"
	"xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"
)

func main() {
	logger.Init(os.Getenv("LOG_LEVEL"))
	log := logger.Module("main")

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

	hub := ws.NewHub(db, cfg.AllowedOrigins, cfg.WSAllowEmptyOrigin)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	cronScheduler := scheduler.NewCronScheduler()
	cronScheduler.Start()
	defer cronScheduler.Stop()

	executorFactory := executor.NewFactory(cfg.RsyncBinary)
	taskManager := task.NewManager(db, executorFactory, hub, cronScheduler, cfg.TaskTrafficRetentionDays, cfg.TaskRunRetentionDays)
	if err := taskManager.LoadSchedules(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("加载定时任务失败")
	}

	prober := probe.NewProber(db, cfg.NodeProbeInterval, cfg.NodeProbeFailThreshold, cfg.NodeProbeConcurrency)
	prober.Start(hubCtx)

	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)
	authService := auth.NewService(db, jwtManager, auth.LoginSecurityConfig{
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
