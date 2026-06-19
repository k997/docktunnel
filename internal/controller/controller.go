package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"

	"docktunnel/internal/docker"
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
	dockerManager     *docker.Manager
	cloudflareManager CloudflareManager
	stateManager      *state.Manager                                                                // State manager for retention policies
	ingressRules      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress // hostname -> rule map
	containerRules    map[string][]string                                                           // containerID -> hostnames map
	containerHealth   map[string]*ContainerHealth                                                   // containerID -> health info
	ruleValidator     RuleValidator
	mu                sync.RWMutex

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
func NewController(dockerManager *docker.Manager, cloudflareManager CloudflareManager, opts ControllerOptions) *Controller {
	controller := &Controller{
		dockerManager:     dockerManager,
		cloudflareManager: cloudflareManager,
		stateManager:      state.NewManager(slog.Default()),
		ingressRules:      make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules:    make(map[string][]string),
		containerHealth:   make(map[string]*ContainerHealth),
		ruleValidator:     NewCompositeValidator(),
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

// registerContainerRules parses labels, validates, and registers ingress rules for a container.
// Returns a map of serviceName → hostname for per-service state management.
func (c *Controller) registerContainerRules(ctx context.Context, event events.Event) (map[string]string, error) {
	parsedRules, err := label.Parse(event.ContainerInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container labels for container %s: %w", event.ContainerID, err)
	}

	c.mu.RLock()
	if err := c.ruleValidator.Validate(parsedRules, c.ingressRules); err != nil {
		c.mu.RUnlock()
		return nil, fmt.Errorf("invalid ingress rules for container %s: %w", event.ContainerID, err)
	}
	c.mu.RUnlock()

	serviceHostnames := make(map[string]string)
	for serviceName, rule := range parsedRules {
		if rule.Hostname.Value != "" {
			c.mu.Lock()
			c.ingressRules[rule.Hostname.Value] = *rule
			c.mu.Unlock()
			serviceHostnames[serviceName] = rule.Hostname.Value
		}
	}

	// Update containerRules for aggregate lookup
	hostnames := make([]string, 0, len(serviceHostnames))
	for _, h := range serviceHostnames {
		hostnames = append(hostnames, h)
	}
	c.mu.Lock()
	c.containerRules[event.ContainerID] = hostnames
	c.mu.Unlock()

	return serviceHostnames, nil
}

// handleContainerStart 处理容器启动事件
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	// 检查容器是否启用了docktunnel
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container start event",
		"action", "dispatch_start",
		"containerID", event.ContainerID,
		"type", event.Type)

	// Restore any retaining/pending entries for this container's services
	c.stateManager.RestoreActiveTunnel(event.ContainerID)

	// 检查容器是否处于抖动状态
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	serviceHostnames, err := c.registerContainerRules(ctx, event)
	if err != nil {
		slog.Error("Failed to register container rules",
			"action", "register_rules",
			"result", "failure",
			"error", err,
			"containerID", event.ContainerID)
		return err
	}

	// Create one TunnelEntry per service with per-service retention policy
	labels := event.ContainerInfo.Config.Labels
	now := time.Now()
	for serviceName, hostname := range serviceHostnames {
		policy := c.getServiceRetentionPolicy(labels, serviceName)
		tunnelEntry := &types.TunnelEntry{
			ContainerID:     event.ContainerID,
			ServiceName:     serviceName,
			RetentionPolicy: policy,
			Status:          types.StatusActive,
			CreatedAt:       now,
			LastSyncAt:      now,
			Config:          types.TunnelConfiguration{Hostname: hostname},
		}
		c.stateManager.AddActiveTunnel(tunnelEntry)
	}

	c.updateContainerHealth(event.ContainerID, true)

	return c.syncToCloudflare(ctx)
}

// getServiceRetentionPolicy parses the retention policy for a specific service.
// Priority: docktunnel.<service>.retention > docktunnel.retention > Immediate.
func (c *Controller) getServiceRetentionPolicy(labels map[string]string, serviceName string) types.RetentionPolicy {
	// Check per-service retention: docktunnel.<serviceName>.retention
	if v, ok := labels["docktunnel."+serviceName+".retention"]; ok {
		if policy, err := label.ParseRetentionPolicy(v); err == nil {
			return policy
		}
	}

	// Check global retention: docktunnel.retention
	if v, ok := labels["docktunnel.retention"]; ok {
		if policy, err := label.ParseRetentionPolicy(v); err == nil {
			return policy
		}
	}

	// Default: Immediate
	return types.RetentionPolicy{Type: types.Immediate}
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

// handleContainerStop 处理容器停止事件
func (c *Controller) handleContainerStop(ctx context.Context, event events.Event) error {
	c.mu.RLock()
	_, hasRules := c.containerRules[event.ContainerID]
	c.mu.RUnlock()

	if !hasRules {
		return nil
	}

	slog.Info("Handling container stop event",
		"action", "dispatch_start",
		"containerID", event.ContainerID,
		"type", event.Type)

	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring stop event", "containerID", event.ContainerID)
		return nil
	}

	// Get all active entries for this container
	entries := c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)

	// Fallback: if no active entries (state lost), create per-service entries from containerRules
	if len(entries) == 0 {
		if event.ContainerInfo != nil && event.ContainerInfo.Config != nil && event.ContainerInfo.Config.Labels != nil {
			labels := event.ContainerInfo.Config.Labels
			parsedRules, err := label.Parse(event.ContainerInfo)
			if err == nil {
				now := time.Now()
				for serviceName := range parsedRules {
					policy := c.getServiceRetentionPolicy(labels, serviceName)
					c.stateManager.AddActiveTunnel(&types.TunnelEntry{
						ContainerID:     event.ContainerID,
						ServiceName:     serviceName,
						RetentionPolicy: policy,
						Status:          types.StatusActive,
						CreatedAt:       now,
						LastSyncAt:      now,
					})
				}
				entries = c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)
			}
		}
		if len(entries) == 0 {
			// Last resort: no info, use Immediate for all hostnames
			c.mu.RLock()
			hostnames := c.containerRules[event.ContainerID]
			c.mu.RUnlock()
			now := time.Now()
			for i, hostname := range hostnames {
				c.stateManager.AddActiveTunnel(&types.TunnelEntry{
					ContainerID:     event.ContainerID,
					ServiceName:     fmt.Sprintf("svc%d", i),
					RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
					Status:          types.StatusActive,
					CreatedAt:       now,
					LastSyncAt:      now,
					Config:          types.TunnelConfiguration{Hostname: hostname},
				})
			}
			entries = c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)
		}
	}

	// Transition each service independently
	var allActions []types.Action
	for _, entry := range entries {
		actions, err := c.stateManager.Transition(
			event.ContainerID, entry.ServiceName,
			types.EventContainerStopped, &entry.RetentionPolicy,
		)
		if err != nil {
			slog.Error("State transition failed",
				"action", "state_transition",
				"result", "failure",
				"container_id", event.ContainerID,
				"service_name", entry.ServiceName,
				"error", err)
			continue
		}
		allActions = append(allActions, actions...)
	}

	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.mu.Unlock()
	c.updateContainerHealth(event.ContainerID, false)

	if err := c.syncToCloudflare(ctx); err != nil {
		// Sync failed — enqueue actions for retry
		for _, action := range allActions {
			if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
				c.stateManager.EnqueueAction(action, err)
			}
		}
		return err
	}
	return nil
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

// handleHealthHealthy re-exposes a container's services when it becomes healthy.
func (c *Controller) handleHealthHealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became healthy, exposing services", "containerID", event.ContainerID)

	// 检查容器是否处于抖动状态
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring health healthy event", "containerID", event.ContainerID)
		return nil
	}

	if _, err := c.registerContainerRules(ctx, event); err != nil {
		slog.Error("Failed to register container rules on health event",
			"action", "register_rules",
			"result", "failure",
			"error", err,
			"containerID", event.ContainerID)
		return err
	}

	// Do NOT call updateContainerHealth — health events don't affect flapping counter
	return c.syncToCloudflare(ctx)
}

// handleHealthUnhealthy removes a container's services when it becomes unhealthy.
func (c *Controller) handleHealthUnhealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became unhealthy, removing services", "containerID", event.ContainerID)

	c.mu.Lock()
	hostnamesToRemove, exists := c.containerRules[event.ContainerID]
	if !exists {
		c.mu.Unlock()
		slog.Debug("No rules found for unhealthy container", "containerID", event.ContainerID)
		return nil
	}

	delete(c.containerRules, event.ContainerID)
	for _, hostname := range hostnamesToRemove {
		delete(c.ingressRules, hostname)
	}
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
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
