package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/config"
	"docktunnel/internal/controller"
	"docktunnel/internal/docker"
	"docktunnel/internal/events"
	"docktunnel/internal/logger"
	"docktunnel/internal/state"
)

var (
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile = flag.String("memprofile", "", "write memory profile to `file`")
)

func main() {
	flag.Parse()

	// Start CPU profiling if requested (T101)
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatalf("Could not create CPU profile: %v", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("Could not start CPU profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	log.Println("DockTunnel starting...")

	// 加载配置
	cfg, err := config.New()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化日志记录器
	var appLogger *slog.Logger = logger.New(cfg.Log.Level, cfg.Log.Format)
	appLogger.Info("Configuration loaded successfully", "log_level", cfg.Log.Level, "log_format", cfg.Log.Format)

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

	// 创建Cloudflare Manager
	cfManager, err := cloudflareManager.NewManager(cfg.GetCloudflareOptions())
	if err != nil {
		slog.Error("Failed to create Cloudflare manager", "error", err)
		os.Exit(1)
	}

	appLogger.Info("Using tunnel", "tunnel", cfManager.GetTunnel().ID)

	// 创建Controller
	controller := controller.NewController(dockerManager, cfManager, cfg.GetControllerOptions())

	// Register gob types for state persistence (T073)
	state.RegisterGobTypes()

	// Load persisted state at startup (T073, T075)
	statePath := cfg.Cleanup.StateFile
	if statePath == "" {
		statePath = state.StateFileDefault
	}
	controller.SetStatePath(statePath)

	// Configure compensation queue parameters
	initialDelay, maxDelay, maxRetries, pollInterval := cfg.GetCompensationConfig()
	controller.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)

	// Configure persistence settings
	backupCount, validateOnLoad := cfg.GetPersistenceConfig()
	controller.SetPersistenceConfig(backupCount, validateOnLoad)

	appLogger.Info("Loading persisted state", "path", statePath)
	if err := controller.LoadState(); err != nil {
		appLogger.Warn("Failed to load persisted state, starting with clean state",
			"error", err)
		// Continue anyway - don't fail startup (T075)
	}

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
				appLogger.Debug("Processing Docker event", "type", event.Type, "containerID", event.ContainerID)
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

	// 启动垃圾回收和对账定时器 (T062)
	wg.Add(1)
	go func() {
		defer wg.Done()

		gcTicker := time.NewTicker(60 * time.Second)
		defer gcTicker.Stop()

		// Reconcile ticker (separate from GC for independent interval control)
		var reconcileTicker *time.Ticker
		if controller.ReconcileEnabled() {
			reconcileTicker = time.NewTicker(controller.ReconcileInterval())
			defer reconcileTicker.Stop()
			appLogger.Info("Starting reconcile ticker",
				"interval", controller.ReconcileInterval())
		}

		appLogger.Info("Starting garbage collection ticker", "interval", "60s")

		for {
			select {
			case <-gcTicker.C:
				appLogger.Debug("Running garbage collection for expired retention policies")
				if err := controller.RunGarbageCollection(ctx); err != nil {
					appLogger.Error("Garbage collection failed", "error", err)
				}
				// Periodically persist state to disk
				if err := controller.SaveStateIfDirty(); err != nil {
					appLogger.Error("Failed to save state", "error", err)
				}

			case <-reconcileTicker.C:
				appLogger.Debug("Running periodic reconciliation")
				if err := controller.Reconcile(ctx); err != nil {
					appLogger.Error("Periodic reconciliation failed", "error", err)
				}

			case <-ctx.Done():
				appLogger.Info("Garbage collection and reconcile ticker stopped")
				return
			}
		}
	}()

	// Start compensation queue background loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		appLogger.Info("Starting compensation queue background loop")
		controller.RunCompensationLoop(ctx)
	}()

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
	// Force save state before shutdown
	if err := controller.ForceSaveState(); err != nil {
		appLogger.Warn("Failed to save state before shutdown", "error", err)
	}

	// Cancel context to notify all goroutines to stop
	cancel()

	// Execute cleanup based on configured strategy
	switch cfg.Cleanup.Strategy {
	case "fast-exit":
		appLogger.Info("Fast exit requested, skipping resource cleanup")
	case "graceful-cleanup":
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cfg.Cleanup.Timeout)
		defer cleanupCancel()

		appLogger.Info("Graceful cleanup requested",
			"timeout", cfg.Cleanup.Timeout)

		if err := controller.CleanupResources(cleanupCtx); err != nil {
			if cleanupCtx.Err() == context.DeadlineExceeded {
				// Collect remaining hostnames for diagnostic logging
				rules := controller.GetIngressRules()
				var remaining []string
				for _, rule := range rules {
					if rule.Hostname.Value != "" && rule.Service.Value != "http_status:404" {
						remaining = append(remaining, rule.Hostname.Value)
					}
				}
				appLogger.Warn("Cleanup timed out",
					"remaining_hostnames", remaining,
					"timeout", cfg.Cleanup.Timeout)
			} else {
				appLogger.Error("Failed to cleanup resources", "error", err)
			}
		} else {
			appLogger.Info("Resources cleaned up successfully")
		}
	default:
		appLogger.Info("Unknown cleanup strategy, skipping cleanup",
			"strategy", cfg.Cleanup.Strategy)
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// Write memory profile if requested (T101)
	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			log.Fatalf("Could not create memory profile: %v", err)
		}
		defer f.Close()
		runtime.ReadMemStats(&memStats)
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatalf("Could not write memory profile: %v", err)
		}
		log.Printf("Memory profile written to %s", *memprofile)
	}

	appLogger.Info("DockTunnel shutdown complete")
}

var memStats runtime.MemStats
