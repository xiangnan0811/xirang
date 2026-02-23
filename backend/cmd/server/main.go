package main

import (
	"context"
	"log"
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
	"xirang/backend/internal/task"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/scheduler"
	"xirang/backend/internal/ws"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	if err := bootstrap.AutoMigrate(db); err != nil {
		log.Fatalf("执行数据库迁移失败: %v", err)
	}
	if err := bootstrap.SeedUsers(db); err != nil {
		log.Fatalf("初始化管理员账号失败: %v", err)
	}

	hub := ws.NewHub(db, cfg.AllowedOrigins, cfg.WSAllowEmptyOrigin)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)

	cronScheduler := scheduler.NewCronScheduler()
	cronScheduler.Start()
	defer cronScheduler.Stop()

	executorFactory := executor.NewFactory(cfg.RsyncBinary)
	taskManager := task.NewManager(db, executorFactory, hub, cronScheduler)
	if err := taskManager.LoadSchedules(context.Background()); err != nil {
		log.Fatalf("加载定时任务失败: %v", err)
	}

	jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)
	authService := auth.NewService(db, jwtManager, auth.LoginSecurityConfig{
		FailLockThreshold: cfg.LoginFailLockThreshold,
		FailLockDuration:  cfg.LoginFailLockDuration,
	})

	router := api.NewRouter(api.Dependencies{
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
		log.Printf("后端服务启动，监听地址: %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务异常退出: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("收到退出信号，开始优雅关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("优雅关闭失败，强制退出: %v", err)
	}
}
