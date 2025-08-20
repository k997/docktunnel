package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/config"
	"docktunnel/internal/controller"
	"docktunnel/internal/docker"
	"docktunnel/internal/events"
	"docktunnel/internal/logger"
	"log/slog"
)

func main() {
	log.Println("DockTunnel starting...")

	// 加载配置
	cfg, err := config.New()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}


	// 初始化日志记录器
	var appLogger *slog.Logger = logger.New(cfg.Log.Level, cfg.Log.Format)
	appLogger.Info("Configuration loaded successfully")

	// 创建上下文用于优雅关闭
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 初始化Docker管理器
	dockerManager, err := docker.NewManager()
	if err != nil {
		appLogger.Error("Failed to create Docker manager", "error", err)
		os.Exit(1)
	}
	defer dockerManager.Close()

	// 初始化Cloudflare管理器
	cfManager, err := cloudflareManager.NewManager(
		cfg.Cloudflare.AccountID,
		cfg.Cloudflare.APIToken,
		cfg.Cloudflare.TunnelID,
		cfg.Cloudflare.TunnelName,
	)
	if err != nil {
		appLogger.Error("Failed to create Cloudflare manager", "error", err)
		os.Exit(1)
	}

	appLogger.Info("Using tunnel", "tunnel", cfManager.GetTunnel())

	// 初始化控制器
	controller := controller.NewController(dockerManager, cfManager, cfg.Cloudflare.CatchAll)

	// 创建事件通道
	eventChan := make(chan events.Event, 10)

	// 启动事件处理循环
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case event := <-eventChan:
				appLogger.Info("Processing Docker event", "type", event.Type, "containerID", event.ContainerID)
				if err := controller.Dispatch(ctx, event); err != nil {
					appLogger.Error("Failed to dispatch event", "error", err)
				}
			case <-ctx.Done():
				appLogger.Info("Event processing loop stopped")
				return
			}
		}
	}()

	// 启动Docker事件监听器
	wg.Add(1)
	go func() {
		defer wg.Done()
		appLogger.Info("Starting Docker event listener")
		if err := dockerManager.ListenForEvents(ctx, eventChan); err != nil {
			appLogger.Error("Docker event listener error", "error", err)
		}
	}()

	// 首次同步配置
	appLogger.Info("Performing initial synchronization")
	if err := controller.Sync(ctx); err != nil {
		appLogger.Error("Initial synchronization failed", "error", err)
	} else {
		appLogger.Info("Initial synchronization completed successfully")
	}

	// 设置系统信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 主循环
	appLogger.Info("DockTunnel started successfully, entering main loop")

	// 启动主事件循环
	for {
		select {
		case <-sigChan:
			appLogger.Info("Shutdown signal received")
			goto shutdown
		case <-ctx.Done():
			appLogger.Info("Context cancelled")
			goto shutdown
		}
	}

shutdown:
	// 取消上下文以通知所有goroutine关闭
	cancel()

	// 如果配置要求清理资源，则执行清理操作
	if cfg.Cleanup.OnExit {
		appLogger.Info("Cleaning up resources as requested in configuration")
		if err := cleanupResources(ctx, cfManager, controller, appLogger); err != nil {
			appLogger.Error("Failed to cleanup resources", "error", err)
		} else {
			appLogger.Info("Resources cleaned up successfully")
		}
	}

	// 等待所有goroutine完成
	wg.Wait()

	appLogger.Info("DockTunnel shutdown complete")
}

// cleanupResources 清理创建的DNS记录和tunnel
func cleanupResources(ctx context.Context, cfManager *cloudflareManager.Manager, controller *controller.Controller, logger *slog.Logger) error {
	// 获取当前的ingress规则以获取所有主机名
	ingressRules := controller.GetIngressRules()

	// 删除所有DNS记录
	for _, rule := range ingressRules {
		if rule.Hostname != "" && rule.Service != "http_status:404" {
			if err := cfManager.DeleteDNSRecord(ctx, rule.Hostname); err != nil {
				logger.Error("Failed to delete DNS record", "hostname", rule.Hostname, "error", err)
				// 继续尝试删除其他记录
			}
		}
	}

	// 注意：我们不删除tunnel本身，因为这可能会影响其他服务
	// 如果需要删除tunnel，用户可以手动删除或通过Cloudflare仪表板操作

	return nil
}
