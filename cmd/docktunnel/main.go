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
	"docktunnel/internal/instance"
	"docktunnel/internal/logger"
	"docktunnel/internal/metrics"
	"docktunnel/internal/server"
	"docktunnel/internal/state"
)

var (
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile = flag.String("memprofile", "", "write memory profile to `file`")
)

// Version information, injected at build time via
// -ldflags "-X main.Version=... -X main.BuildDate=... -X main.GitCommit=...".
var (
	Version   = "dev"
	BuildDate = ""
	GitCommit = ""
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
	appLogger.Info("DockTunnel starting",
		"version", Version, "git_commit", GitCommit, "build_date", BuildDate,
		"log_level", cfg.Log.Level, "log_format", cfg.Log.Format)

	// Acquire single-instance lock before touching Cloudflare. Two instances
	// against the same tunnel would race on configuration writes and thrash
	// DNS records. Released implicitly on process exit; deferred Close keeps
	// the next startup from waiting on fd cleanup.
	instanceLock, err := instance.Acquire(cfg.Cleanup.LockFile)
	if err != nil {
		appLogger.Error("Failed to acquire instance lock", "path", cfg.Cleanup.LockFile, "error", err)
		os.Exit(1)
	}
	defer instanceLock.Close()
	appLogger.Info("Acquired instance lock", "path", cfg.Cleanup.LockFile)

	// 创建上下文用于优雅关闭
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Verify the Cloudflare API token up front so a misconfigured token fails
	// fast with a clear message instead of bubbling up through tunnel creation.
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 15*time.Second)
	if err := cfg.VerifyAPIToken(verifyCtx, nil); err != nil {
		appLogger.Error("Cloudflare API token verification failed", "error", err)
		verifyCancel()
		os.Exit(1)
	}
	verifyCancel()
	appLogger.Info("Cloudflare API token verified")

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
		appLogger.Error("Failed to create Cloudflare manager", "error", err)
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
	controller.SetCompensationQueueCap(cfg.GetCompensationQueueCap())

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
	// 256 is large enough to absorb container storms (bulk restarts, daemon
	// reconnect bursts) without blocking the Docker event-listener goroutine.
	// trySendEvent in monitor.go drops + counts when even this fills up.
	eventChan := make(chan events.Event, 256)

	var wg sync.WaitGroup

	// Watchdog: sample EventsDropped every 30s. Any non-zero delta since
	// the last sample surfaces as a WARN so operators don't have to scrape
	// Prometheus to learn events are being lost. Pipeline saturation is
	// otherwise invisible until reconcile catches up (default 120s later).
	wg.Add(1)
	go runWithRecover("event-drop-watchdog", func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		last := metrics.EventsDroppedCounterValue()
		for {
			select {
			case <-ticker.C:
				cur := metrics.EventsDroppedCounterValue()
				if delta := cur - last; delta > 0 {
					appLogger.Warn("Events dropped in last 30s — pipeline saturated",
						"delta", delta, "total", cur,
						"hint", "consider lowering reconcile interval or scaling up the controller")
				}
				last = cur
			case <-ctx.Done():
				return
			}
		}
	})

	// 启动Docker事件监听器 goroutine（只灌 channel，不处理事件）。
	// 放在初始 Sync 之前：监听器只会向 channel 投递事件，真正的处理在
	// dispatch 循环启动后才开始，因此初始 Sync 的全量重建不会覆盖
	// 并发到达的事件（B6 启动竞态修复）。
	wg.Add(1)
	go runWithRecover("docker-event-listener", func() {
		defer wg.Done()
		appLogger.Info("Starting Docker event listener")
		if err := dockerManager.ListenForEvents(ctx, eventChan); err != nil {
			appLogger.Error("Docker event listener error", "error", err)
		}
	})

	// 首次同步配置 — 在事件 dispatch 循环启动之前执行，先建立完整期望状态。
	appLogger.Info("Performing initial synchronization")
	if err := controller.Sync(ctx); err != nil {
		appLogger.Error("Initial synchronization failed", "error", err)
	} else {
		appLogger.Info("Initial synchronization completed successfully")
	}

	// 启动事件处理循环 — 最后启动（B6）：初始 Sync 完成后才开始处理
	// 排队的事件，避免事件处理器与全量重建互相覆盖。
	wg.Add(1)
	go runWithRecover("event-dispatch-loop", func() {
		defer wg.Done()
		for {
			select {
			case event := <-eventChan:
				// 单个事件的 panic 只记录并继续（B7）
				func() {
					defer func() {
						if r := recover(); r != nil {
							appLogger.Error("Panic recovered while dispatching event",
								"type", event.Type,
								"containerID", event.ContainerID,
								"panic", r)
						}
					}()
					appLogger.Debug("Processing Docker event", "type", event.Type, "containerID", event.ContainerID)
					if err := controller.Dispatch(ctx, event); err != nil {
						appLogger.Error("Failed to dispatch event", "error", err)
					}
				}()
			case <-ctx.Done():
				appLogger.Info("Event processing loop stopped")
				return
			}
		}
	})

	// Start HTTP server for metrics and diagnostics (Phase 6)
	debugServer := server.New(cfg.GetServerAddr(), cfg.Server.DebugToken, controller.GetDebugState)
	wg.Add(1)
	go runWithRecover("debug-http-server", func() {
		defer wg.Done()
		appLogger.Info("Starting diagnostics HTTP server", "addr", cfg.GetServerAddr())
		if err := debugServer.Start(ctx); err != nil {
			appLogger.Error("Diagnostics HTTP server stopped with error", "error", err)
		}
	})

	// 启动垃圾回收和对账定时器 (T062)
	wg.Add(1)
	go runWithRecover("gc-reconcile-ticker", func() {
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
				reconcileStart := time.Now()
				if err := controller.Reconcile(ctx); err != nil {
					appLogger.Error("Periodic reconciliation failed", "error", err)
				}
				metrics.ObserveReconcile(time.Since(reconcileStart).Seconds())

			case <-ctx.Done():
				appLogger.Info("Garbage collection and reconcile ticker stopped")
				return
			}
		}
	})

	// Start the sync worker. Wrapper goroutine exists so wg.Wait() in
	// shutdown blocks until the worker has fully stopped, not just
	// until Start returns.
	wg.Add(1)
	go runWithRecover("sync-worker", func() {
		defer wg.Done()
		appLogger.Info("Starting sync worker")
		controller.Start(ctx)
		<-ctx.Done()
		controller.StopSyncWorker()
	})

	// Start compensation queue background loop
	wg.Add(1)
	go runWithRecover("compensation-loop", func() {
		defer wg.Done()
		appLogger.Info("Starting compensation queue background loop")
		controller.RunCompensationLoop(ctx)
	})

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

	// Execute cleanup BEFORE cancelling the root ctx. CleanupResources
	// uses FlushSync to push the cleared-state config through the sync
	// worker, but the worker wrapper goroutine exits the moment ctx is
	// cancelled. If we cancel first, FlushSync's send on triggerCh
	// blocks forever because nothing is left to drain it, the cleared
	// config never lands on Cloudflare, and every graceful-cleanup
	// shutdown hangs for the full cleanup timeout.
	//
	// B5: cleanup runs only when cleanup.onExit=true AND the strategy is
	// graceful-cleanup; otherwise it is skipped (with a Warn for non-default
	// strategies) so force-cleanup/none/fast-exit configs don't delete DNS.
	if shouldSkipCleanup(cfg.Cleanup.OnExit, cfg.Cleanup.Strategy) {
		if !cfg.Cleanup.OnExit {
			appLogger.Info("skipping cleanup (cleanup.onExit=false)")
		} else {
			appLogger.Warn("Skipping cleanup: strategy is not graceful-cleanup",
				"strategy", cfg.Cleanup.Strategy)
		}
	} else {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cfg.Cleanup.Timeout)
		defer cleanupCancel()

		appLogger.Info("Graceful cleanup requested",
			"timeout", cfg.Cleanup.Timeout)

		if err := controller.CleanupResources(cleanupCtx); err != nil {
			if cleanupCtx.Err() == context.DeadlineExceeded {
				// Collect remaining hostnames for diagnostic logging
				rules := controller.GetIngressRules()
				var remaining []string
				catchAll := controller.GetCatchAllService()
				for _, rule := range rules {
					if rule.Hostname.Value != "" && rule.Service.Value != catchAll {
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
	}

	// Cancel context to notify all goroutines to stop. Runs after
	// cleanup so the sync worker is still alive to drain FlushSync's
	// trigger.
	cancel()

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

// runWithRecover runs f, recovering any panic and logging it so a single
// goroutine failure (event dispatch, listener, watchdog, ticker, ...) cannot
// take down the whole process (B7).
func runWithRecover(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered; goroutine continues",
				"goroutine", name,
				"panic", r)
		}
	}()
	f()
}

// shouldSkipCleanup reports whether shutdown should skip resource cleanup
// entirely. Cleanup runs only when cleanup.onExit=true AND the strategy is
// graceful-cleanup; onExit=false, fast-exit and unknown strategies all skip
// (B5). Config validation currently still accepts force-cleanup/none — they
// are treated as unknown here and skipped with a warning.
func shouldSkipCleanup(onExit bool, strategy string) bool {
	if !onExit {
		return true
	}
	return strategy != "graceful-cleanup"
}
