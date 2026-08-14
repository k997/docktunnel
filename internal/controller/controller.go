package controller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/events"
	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// CloudflareManager defines the interface for Cloudflare tunnel management
type CloudflareManager interface {
	GetTunnel() *zero_trust.TunnelCloudflaredGetResponse
	GetConfiguration(ctx context.Context) ([]zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress, error)
	UpdateConfiguration(ctx context.Context, ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error
	ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error)
	DeleteDNSRecords(ctx context.Context, hostnames []string) error
	UpsertDNSRecords(ctx context.Context, hostnames []string) error
}

// DockerScanner abstracts the Docker manager's container-scanning capability.
// Kept narrow so controller tests can substitute a fake.
type DockerScanner interface {
	ScanRunningContainers(ctx context.Context) ([]events.Event, error)
}

// ControllerOptions 用于配置Controller的选项
type ControllerOptions struct {
	CatchAllService string
	// 容器抖动检测配置
	FlappingWindow    time.Duration // 检测窗口期
	FlappingThreshold int           // 窗口期内重启阈值
	CoolingPeriod     time.Duration // 基础冷却期
	MaxCoolingPeriod  time.Duration // 最大冷却期
	// 防抖配置
	DebounceDuration time.Duration // 防抖持续时间
	// 对账配置
	ReconcileEnabled  bool
	ReconcileInterval time.Duration
}

// Controller 负责协调Docker和Cloudflare模块的工作
type Controller struct {
	dockerManager     DockerScanner
	cloudflareManager CloudflareManager
	stateManager      *state.Manager                                                                // State manager for retention policies
	ingressRules      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress // key: ingressKey(hostname, path)
	containerRules    map[string][]string                                                           // containerID -> hostnames map
	containerHealth   map[string]*ContainerHealth                                                   // containerID -> health info
	ruleValidator     RuleValidator
	mu                sync.RWMutex

	// catchAllService is the service value for the trailing catch-all ingress
	// rule (e.g. "http_status:404"). Empty means default.
	catchAllService string

	// 熔断器配置 — retained on Controller for &Controller{} test-literal
	// compat (see TestNewController, TestStartupReconciliation). Production
	// access is via healthTracker methods (health.go).
	flappingWindow    time.Duration // 检测窗口期
	flappingThreshold int           // 窗口期内重启阈值
	coolingPeriod     time.Duration // 基础冷却期
	maxCoolingPeriod  time.Duration // 最大冷却期

	// 防抖配置
	debounceDuration time.Duration // 防抖持续时间
	// syncWorker serializes all Cloudflare writes through a single
	// goroutine. See sync_worker.go.
	syncWorker *syncWorker
	// 对账配置
	reconcileEnabled  bool
	reconcileInterval time.Duration

	// Internal helpers (Phase 2 extraction)
	dispatcher        *dispatcher
	reconciler        *reconciler
	gc                *gc
	compensation      *compensation
	diagnosticsHelper *diagnosticsHelper
	syncer            *syncer
	healthTracker     *healthTracker
}

// NewController 创建一个新的控制器实例
func NewController(dockerManager DockerScanner, cloudflareManager CloudflareManager, opts ControllerOptions) *Controller {
	controller := &Controller{
		dockerManager:     dockerManager,
		cloudflareManager: cloudflareManager,
		stateManager:      state.NewManager(slog.Default()),
		ingressRules:      make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules:    make(map[string][]string),
		containerHealth:   make(map[string]*ContainerHealth),
		ruleValidator:     NewCompositeValidator(),
		catchAllService:   opts.CatchAllService,
		flappingWindow:    opts.FlappingWindow,
		flappingThreshold: opts.FlappingThreshold,
		coolingPeriod:     opts.CoolingPeriod,
		maxCoolingPeriod:  opts.MaxCoolingPeriod,
		debounceDuration:  opts.DebounceDuration,
		reconcileEnabled:  opts.ReconcileEnabled,
		reconcileInterval: opts.ReconcileInterval,
	}

	// 如果没有提供配置参数，则使用默认值
	if controller.flappingWindow == 0 {
		controller.flappingWindow = 1 * time.Minute
	}
	if controller.flappingThreshold == 0 {
		controller.flappingThreshold = 5
	}
	if controller.coolingPeriod == 0 {
		controller.coolingPeriod = 5 * time.Minute
	}
	if controller.maxCoolingPeriod == 0 {
		controller.maxCoolingPeriod = 30 * time.Minute
	}
	if controller.debounceDuration == 0 {
		controller.debounceDuration = 2 * time.Second
	}
	if controller.reconcileInterval == 0 {
		controller.reconcileInterval = 120 * time.Second
	}

	controller.syncer = newSyncer(controller, slog.Default())
	controller.syncWorker = newSyncWorker(controller.debounceDuration, controller.syncer.performSync, slog.Default())

	// Wire sync failures into the compensation queue: whenever the worker's
	// syncFn returns an error, enqueue an ActionSync so the failure is retried
	// even if no further event arrives (B3). Deduplicate: if an unfinished
	// ActionSync is already queued, don't stack another one.
	controller.syncWorker.SetOnError(func(err error) {
		if !controller.stateManager.HasPendingActionKind(types.ActionSync) {
			controller.stateManager.EnqueueAction(types.Action{Kind: types.ActionSync}, err)
		}
	})

	controller.dispatcher = newDispatcher(controller)
	controller.reconciler = newReconciler(controller, slog.Default())
	controller.gc = newGC(controller, slog.Default())
	controller.compensation = newCompensation(controller, slog.Default())
	controller.diagnosticsHelper = newDiagnosticsHelper(controller)
	controller.healthTracker = newHealthTracker(controller, slog.Default())

	return controller
}

// Start launches background goroutines requiring explicit lifecycle
// management. Currently just the sync worker. Called from main.go
// after NewController.
func (c *Controller) Start(ctx context.Context) {
	c.syncWorker.Start(ctx)
}

// StopSyncWorker blocks until the sync worker goroutine has exited.
// Called from main.go during shutdown.
func (c *Controller) StopSyncWorker() {
	c.syncWorker.Stop()
}

// Dispatch 是所有事件处理的统一入口
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
	return c.dispatcher.Dispatch(ctx, event)
}

// CleanupResources 清理创建的DNS记录
func (c *Controller) CleanupResources(ctx context.Context) error {
	c.mu.Lock()
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	c.containerRules = make(map[string][]string)
	c.mu.Unlock()

	slog.Info("Cleaning up resources: cleared internal rules")

	if err := c.syncer.FlushSync(ctx); err != nil {
		slog.Error("Failed to perform cleanup sync", "error", err, "result", "failure")
		return err
	}

	slog.Info("Resource cleanup completed successfully")
	return nil
}

// isDocktunnelEnabled delegates to dispatcher. Temporary; will be inlined
// into handlers.go once handlers are extracted.
func (c *Controller) isDocktunnelEnabled(event events.Event) bool {
	return c.dispatcher.isDocktunnelEnabled(event)
}

// getCatchAllService returns the service value used for the trailing
// catch-all ingress rule, falling back to http_status:404 when unset (B4).
func (c *Controller) getCatchAllService() string {
	if c.catchAllService != "" {
		return c.catchAllService
	}
	return "http_status:404"
}

// GetCatchAllService exposes the configured catch-all service to callers
// outside the controller package (e.g. cmd/docktunnel diagnostics).
func (c *Controller) GetCatchAllService() string {
	return c.getCatchAllService()
}

// SetStatePath sets the path for state persistence
func (c *Controller) SetStatePath(path string) {
	c.stateManager.SetStatePath(path)
}

// LoadState loads persisted state from disk (T073, T075)
func (c *Controller) LoadState() error {
	return c.stateManager.Load("")
}

// SaveState saves the current state to disk
func (c *Controller) SaveState() error {
	return c.stateManager.Save("")
}

// SaveStateIfDirty saves state only if it has changed and enough time has passed
func (c *Controller) SaveStateIfDirty() error {
	return c.stateManager.SaveIfDirty()
}

// ForceSaveState forces an immediate state save regardless of dirty flag
func (c *Controller) ForceSaveState() error {
	return c.stateManager.ForceSave()
}

// ExecuteAction executes a single compensation action.
func (c *Controller) ExecuteAction(ctx context.Context, action types.Action) error {
	if c.compensation == nil {
		c.compensation = newCompensation(c, slog.Default())
	}
	return c.compensation.ExecuteAction(ctx, action)
}

// RunCompensationLoop starts the compensation queue background loop.
func (c *Controller) RunCompensationLoop(ctx context.Context) {
	c.compensation.RunCompensationLoop(ctx)
}

// SetCompensationConfig configures the compensation queue parameters.
func (c *Controller) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	c.compensation.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}

// SetCompensationQueueCap sets the maximum compensation queue size.
func (c *Controller) SetCompensationQueueCap(size int) {
	c.compensation.SetCompensationQueueCap(size)
}

// SetPersistenceConfig configures backup and validation settings.
func (c *Controller) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	c.compensation.SetPersistenceConfig(backupCount, validateOnLoad)
}

// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (c *Controller) Sync(ctx context.Context) error {
	if c.syncer == nil {
		c.syncer = newSyncer(c, slog.Default())
	}
	return c.syncer.Sync(ctx)
}

// GetIngressRules 获取当前的Ingress规则
func (c *Controller) GetIngressRules() []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	if c.syncer == nil {
		c.syncer = newSyncer(c, slog.Default())
	}
	return c.syncer.GetIngressRules()
}

// syncToCloudflare signals the sync worker.
func (c *Controller) syncToCloudflare(ctx context.Context) error {
	if c.syncer == nil {
		c.syncer = newSyncer(c, slog.Default())
	}
	return c.syncer.syncToCloudflare(ctx)
}

// refreshActualState delegates to syncer.
func (c *Controller) refreshActualState(ctx context.Context) {
	if c.syncer == nil {
		c.syncer = newSyncer(c, slog.Default())
	}
	c.syncer.refreshActualState(ctx)
}

// setLastKnownActualRules replaces the cached actual-state snapshot.
// Test-visible (TestSetLastKnownActualRules_UpdatesCache uses &Controller{}).
func (c *Controller) setLastKnownActualRules(rules []diagnostics.RuleView) {
	if c.syncer == nil {
		c.syncer = newSyncer(c, slog.Default())
	}
	c.syncer.setLastKnownActualRules(rules)
}

// isFlapping delegates to healthTracker.
func (c *Controller) isFlapping(containerID string) bool {
	if c.healthTracker == nil {
		c.healthTracker = newHealthTracker(c, slog.Default())
	}
	return c.healthTracker.isFlapping(containerID)
}

// updateContainerHealth delegates to healthTracker.
func (c *Controller) updateContainerHealth(containerID string, isStartEvent bool) {
	if c.healthTracker == nil {
		c.healthTracker = newHealthTracker(c, slog.Default())
	}
	c.healthTracker.updateContainerHealth(containerID, isStartEvent)
}

// RunGarbageCollection runs garbage collection for expired retention policies.
func (c *Controller) RunGarbageCollection(ctx context.Context) error {
	return c.gc.RunGarbageCollection(ctx)
}

// handleResync performs a full state synchronization after Docker daemon reconnection.
func (c *Controller) handleResync(ctx context.Context) error {
	return c.reconciler.handleResync(ctx)
}

// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
func (c *Controller) Reconcile(ctx context.Context) error {
	return c.reconciler.Reconcile(ctx)
}

// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (c *Controller) ReconcileEnabled() bool {
	return c.reconciler.ReconcileEnabled()
}

// ReconcileInterval returns the reconciliation interval.
func (c *Controller) ReconcileInterval() time.Duration {
	return c.reconciler.ReconcileInterval()
}

// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
func (c *Controller) GetDebugState() diagnostics.DebugStateResponse {
	if c.diagnosticsHelper == nil {
		c.diagnosticsHelper = newDiagnosticsHelper(c)
	}
	return c.diagnosticsHelper.GetDebugState()
}
