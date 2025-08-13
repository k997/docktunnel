package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"docktunnel/internal/config"
	"docktunnel/internal/controller"
	"docktunnel/internal/docker"
	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/logger"
)

func main() {
	log.Println("DockTunnel starting...")

	// 加载配置
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}

	// 初始化日志记录器
	logger := logger.New(cfg.GetLogLevel(), cfg.GetLogFormat())
	logger.Info("Configuration loaded successfully")

	// 创建上下文用于优雅关闭
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 初始化Docker管理器
	dockerManager, err := docker.NewManager()
	if err != nil {
		logger.Error("Failed to create Docker manager", "error", err)
		os.Exit(1)
	}
	defer dockerManager.Close()

	// 初始化Cloudflare管理器
	cfManager, err := cloudflareManager.NewManager(
		cfg.Cloudflare.AccountID,
		cfg.Cloudflare.APIToken,
		cfg.Cloudflare.TunnelID,
	)
	if err != nil {
		logger.Error("Failed to create Cloudflare manager", "error", err)
		os.Exit(1)
	}

	// 验证Cloudflare连接
	if err := cfManager.ValidateConnection(ctx); err != nil {
		logger.Error("Failed to validate Cloudflare connection", "error", err)
		os.Exit(1)
	}

	// 初始化控制器
	controller := controller.NewController(dockerManager, cfManager, "DockTunnel")

	// 创建用于接收Docker事件的channel
	updateChan := make(chan struct{}, 1)

	// 启动Docker事件监听器
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("Starting Docker event listener")
		if err := dockerManager.ListenForEvents(ctx, updateChan); err != nil {
			logger.Error("Docker event listener error", "error", err)
		}
	}()

	// 首次同步配置
	logger.Info("Performing initial synchronization")
	if err := controller.Sync(ctx); err != nil {
		logger.Error("Initial synchronization failed", "error", err)
	} else {
		logger.Info("Initial synchronization completed successfully")
	}

	// 设置系统信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 主循环
	logger.Info("DockTunnel started successfully, entering main loop")

	// 启动主事件循环
	for {
		select {
		case <-updateChan:
			logger.Info("Docker event received, triggering synchronization")
			// 添加一个小的延迟，以防止在容器启动/停止时过于频繁地触发同步
			time.Sleep(2 * time.Second)
			
			if err := controller.Sync(ctx); err != nil {
				logger.Error("Synchronization failed", "error", err)
			} else {
				logger.Info("Synchronization completed successfully")
			}
		case <-sigChan:
			logger.Info("Shutdown signal received")
			goto shutdown
		case <-ctx.Done():
			logger.Info("Context cancelled")
			goto shutdown
		}
	}

shutdown:
	// 取消上下文以通知所有goroutine关闭
	cancel()
	
	// 等待所有goroutine完成
	wg.Wait()
	
	logger.Info("DockTunnel shutdown complete")
}