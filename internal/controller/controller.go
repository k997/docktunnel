package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/internal/metrics"
	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	eventTypes "github.com/docker/docker/api/types/events"

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

// ContainerHealth 记录容器的健康状态信息
type ContainerHealth struct {
	RestartCount int       // 重启次数
	LastRestart  time.Time // 上次重启时间
	IsFlapping   bool      // 是否处于抖动状态
	CoolingUntil time.Time // 冷却期结束时间
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

	// 熔断器配置
	flappingWindow    time.Duration // 检测窗口期
	flappingThreshold int           // 窗口期内重启阈值
	coolingPeriod     time.Duration // 基础冷却期
	maxCoolingPeriod  time.Duration // 最大冷却期

	// 防抖配置
	debounceTimer    *time.Timer   // 防抖计时器
	debounceDuration time.Duration // 防抖持续时间
	pendingUpdates   bool          // 是否有待处理的更新
	// 对账配置
	reconcileEnabled  bool
	reconcileInterval time.Duration

	// Cached actual state for /debug/state diagnostics endpoint.
	// Updated after successful UpdateConfiguration calls.
	actualStateMu        sync.RWMutex
	lastKnownActualRules []diagnostics.RuleView
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
		pendingUpdates:    false,
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

	return controller
}

// Dispatch 是所有事件处理的统一入口
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
	err := c.dispatchInner(ctx, event)

	// Record outcome for /metrics (Phase 6)
	result := "success"
	if err != nil {
		result = "failure"
	}
	metrics.RecordEvent(string(event.Type), result)

	return err
}

// dispatchInner is the original dispatch switch.
func (c *Controller) dispatchInner(ctx context.Context, event events.Event) error {
	switch event.Type {
	case eventTypes.ActionStart:
		return c.handleContainerStart(ctx, event)
	case eventTypes.ActionStop:
		return c.handleContainerStop(ctx, event)
	case eventTypes.ActionDie:
		return c.handleContainerStop(ctx, event)
	case events.ActionHealthHealthy:
		return c.handleHealthHealthy(ctx, event)
	case events.ActionHealthUnhealthy:
		return c.handleHealthUnhealthy(ctx, event)
	case events.ActionHealthStarting:
		return c.handleHealthUnhealthy(ctx, event)
	case events.ActionResync:
		return c.handleResync(ctx)
	default:
		// 忽略不关心的事件
		return nil
	}
}

// CleanupResources 清理创建的DNS记录
func (c *Controller) CleanupResources(ctx context.Context) error {
	c.mu.Lock()
	// 清空ingressRules和containerRules
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	c.containerRules = make(map[string][]string)
	c.mu.Unlock()

	slog.Info("Cleaning up resources: cleared internal rules")

	// 调用performSync同步空的规则集（这将删除所有DNS记录）
	if err := c.performSync(ctx); err != nil {
		slog.Error("Failed to perform cleanup sync", "error", err)
		return err
	}

	slog.Info("Resource cleanup completed successfully")

	// 注意：我们不删除tunnel本身，因为这可能会影响其他服务
	// 如果需要删除tunnel，用户可以手动删除或通过Cloudflare仪表板操作

	return nil
}

// isDocktunnelEnabled 检查容器是否启用了docktunnel
func (c *Controller) isDocktunnelEnabled(event events.Event) bool {
	// 检查容器信息是否存在
	if event.ContainerInfo == nil || event.ContainerInfo.Config == nil || event.ContainerInfo.Config.Labels == nil {
		return false
	}

	// 检查docktunnel.enable标签是否设置为true
	return event.ContainerInfo.Config.Labels["docktunnel.enable"] == "true"
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
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// Restore any retaining/pending entries for this container's services
	c.stateManager.RestoreActiveTunnel(event.ContainerID)

	// 检查容器是否处于抖动状态
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	serviceHostnames, err := c.registerContainerRules(ctx, event)
	if err != nil {
		slog.Error("Failed to register container rules", "error", err, "containerID", event.ContainerID)
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

	c.mu.Lock()
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()

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

	slog.Info("Handling container stop event", "containerID", event.ContainerID)

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
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

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

// ExecuteAction executes a single action (e.g., delete route and sync).
// Used by the compensation queue to retry failed actions.
func (c *Controller) ExecuteAction(ctx context.Context, action types.Action) error {
	if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
		c.mu.Lock()
		delete(c.ingressRules, action.Hostname)
		c.mu.Unlock()
	}
	return c.syncToCloudflare(ctx)
}

// RunCompensationLoop starts the compensation queue background loop.
// Blocks until ctx is cancelled.
func (c *Controller) RunCompensationLoop(ctx context.Context) {
	executor := func(action types.Action) error {
		return c.ExecuteAction(ctx, action)
	}
	c.stateManager.RunCompensation(ctx, executor)
}

// SetCompensationConfig configures the compensation queue parameters.
func (c *Controller) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	c.stateManager.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}

// SetPersistenceConfig configures backup and validation settings.
func (c *Controller) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	c.stateManager.SetBackupCount(backupCount)
	c.stateManager.SetValidateOnLoad(validateOnLoad)
}

// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (c *Controller) Sync(ctx context.Context) error {
	// 从cloudflareManager获取tunnel信息
	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Starting synchronization", "tunnelID", tunnel.ID)

	// 扫描运行中的容器
	eventsList, err := c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to scan running containers: %w", err)
	}

	slog.Info("Found containers with docktunnel labels", "count", len(eventsList))

	// Reconcile with persisted state (T074)
	// Detect containers that started during downtime and restore them if needed
	reconciledCount := 0
	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}

		containerID := event.ContainerID

		// Check if container is in pending deletions (was stopped, now restarted)
		if pendingEntry, exists := c.stateManager.GetPendingDeletion(containerID); exists {
			slog.Info("Container restarted during downtime, restoring from pending deletion",
				"containerID", containerID,
				"service_name", pendingEntry.ServiceName,
			)

			// Restore to active state
			if err := c.stateManager.RestoreActiveTunnel(containerID); err != nil {
				slog.Warn("Failed to restore active tunnel for container",
					"containerID", containerID,
					"error", err)
			} else {
				reconciledCount++
			}
		}
	}

	if reconciledCount > 0 {
		slog.Info("Reconciled containers from pending deletion state", "count", reconciledCount)
	}

	// 收集所有ingress规则
	allParsedRules := make(map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	containerHostnames := make(map[string][]string) // containerID -> hostnames

	for _, event := range eventsList {
		// 检查容器是否启用了docktunnel
		if !c.isDocktunnelEnabled(event) {
			continue
		}

		// 解析标签获取主机名
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse container labels during sync", "containerID", event.ContainerID, "error", err)
			continue
		}

		// 合并规则，确保服务名唯一
		for serviceName, rule := range parsedRules {
			if _, exists := allParsedRules[serviceName]; exists {
				slog.Warn("Duplicate service name found during sync, skipping.", "serviceName", serviceName, "containerID", event.ContainerID)
				continue
			}
			allParsedRules[serviceName] = rule

			// Populate per-service state entries if not already present
			if _, exists := c.stateManager.GetActiveTunnel(event.ContainerID, serviceName); !exists {
				labels := event.ContainerInfo.Config.Labels
				policy := c.getServiceRetentionPolicy(labels, serviceName)
				hostname := ""
				if rule.Hostname.Value != "" {
					hostname = rule.Hostname.Value
				}
				c.stateManager.AddActiveTunnel(&types.TunnelEntry{
					ContainerID:     event.ContainerID,
					ServiceName:     serviceName,
					RetentionPolicy: policy,
					Status:          types.StatusActive,
					CreatedAt:       time.Now(),
					LastSyncAt:      time.Now(),
					Config:          types.TunnelConfiguration{Hostname: hostname},
				})
			}
		}

		hostnames := make([]string, 0, len(parsedRules))
		for _, rule := range parsedRules {
			if rule.Hostname.Value != "" {
				hostnames = append(hostnames, rule.Hostname.Value)
			}
		}

		if len(hostnames) > 0 {
			containerHostnames[event.ContainerID] = hostnames
		}
	}

	// 验证所有规则
	if err := c.ruleValidator.Validate(allParsedRules, nil); err != nil {
		slog.Error("Invalid ingress rules during sync", "error", err)
		return fmt.Errorf("invalid ingress rules during sync: %w", err)
	}

	// 更新内部状态
	c.mu.Lock()
	// 重新创建ingress规则map
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)

	// 添加所有新规则
	for _, rule := range allParsedRules {
		// 添加所有有主机名的规则
		if rule.Hostname.Value != "" {
			c.ingressRules[rule.Hostname.Value] = *rule
		}
	}

	// 更新容器与主机名的关联关系
	c.containerRules = containerHostnames
	c.mu.Unlock()

	// 同步到Cloudflare
	if err := c.syncToCloudflare(ctx); err != nil {
		return err
	}

	// Refresh the actual-state cache for /debug/state (Phase 6).
	// Non-fatal: a failure here just means diagnostics show stale data.
	c.refreshActualState(ctx)

	return nil
}

// syncToCloudflare 将当前规则同步到Cloudflare
func (c *Controller) syncToCloudflare(ctx context.Context) error {
	c.mu.Lock()

	// 标记有待处理的更新
	c.pendingUpdates = true

	// 如果防抖计时器已经存在，停止它并重新开始计时
	if c.debounceTimer != nil {
		c.debounceTimer.Stop()
	}

	// 创建新的防抖计时器
	c.debounceTimer = time.AfterFunc(c.debounceDuration, func() {
		c.mu.Lock()
		c.pendingUpdates = false
		c.mu.Unlock()

		// 在单独的goroutine中执行实际的同步操作
		go c.performSync(context.Background())
	})

	c.mu.Unlock()

	// 立即返回，不等待同步完成
	return nil
}

// performSync 执行实际的Cloudflare同步操作
func (c *Controller) performSync(ctx context.Context) error {
	// 构建规则列表
	ingressRules := c.GetIngressRules()

	slog.Info("Performing sync with rules", "ruleCount", len(ingressRules))

	// 从cloudflareManager获取tunnel信息
	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		slog.Error("Tunnel is not available")
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Using tunnel", "tunnelID", tunnel.ID)

	// 更新配置（保持原始的ingress规则，不需要修改Service字段）
	if err := c.cloudflareManager.UpdateConfiguration(ctx, ingressRules); err != nil {
		slog.Error("Failed to update tunnel configuration", "error", err)
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "ruleCount", len(ingressRules)-1) // -1 for catch-all rule

	// 同步DNS记录
	if err := c.syncDNSRecords(ctx); err != nil {
		slog.Error("Failed to sync DNS records", "error", err)
		return fmt.Errorf("failed to sync DNS records: %w", err)
	}

	slog.Info("Sync operation completed successfully")
	return nil
}

// syncDNSRecords 同步DNS记录到Cloudflare
// 这个方法会确保Cloudflare中的DNS记录与当前ingress规则保持一致
// 使用批量API操作来减少对Cloudflare的访问压力
func (c *Controller) syncDNSRecords(ctx context.Context) error {
	// 获取当前隧道信息
	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Debug("Starting DNS records sync", "tunnelID", tunnel.ID)

	c.mu.RLock()
	// Collect hostnames from ingressRules (source of truth for active routes).
	// This correctly preserves DNS for Timed/Forever retention where
	// containerRules is cleared but ingressRules are kept.
	currentHostnames := make(map[string]bool)
	for hostname := range c.ingressRules {
		currentHostnames[hostname] = true
	}
	c.mu.RUnlock()

	slog.Info("Syncing DNS records", "expectedHostnamesCount", len(currentHostnames), "expectedHostnames", currentHostnames)

	// 先获取Cloudflare上当前的所有DNS记录，以减少API访问次数
	allTunnelRecords, err := c.cloudflareManager.ListDNSRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tunnel DNS records: %w", err)
	}

	slog.Debug("Found existing tunnel DNS records", "count", len(allTunnelRecords))
	for _, record := range allTunnelRecords {
		slog.Debug("Existing DNS record", "name", record.Name, "content", record.Content, "id", record.ID)
	}

	// 创建一个映射以便快速查找现有的DNS记录
	existingRecords := make(map[string]dns.RecordResponse)
	for _, record := range allTunnelRecords {
		existingRecords[record.Name] = record
	}

	// 收集需要创建/更新和删除的DNS记录
	var upsertHostnames []string
	var deleteHostnames []string

	expectedContent := fmt.Sprintf("%s.cfargotunnel.com", tunnel.ID)

	// 处理需要的DNS记录（收集需要创建或更新的记录）
	for hostname := range currentHostnames {
		// 当记录不存在或内容不一致时需要处理
		if record, exists := existingRecords[hostname]; !exists || record.Content != expectedContent {
			upsertHostnames = append(upsertHostnames, hostname)
		}
	}

	// 收集需要删除的DNS记录（精确匹配cfargotunnel.com格式）
	for hostname, record := range existingRecords {
		if !currentHostnames[hostname] && strings.HasSuffix(record.Content, ".cfargotunnel.com") {
			// 记录存在但不再需要，添加到删除列表
			deleteHostnames = append(deleteHostnames, hostname)
		}
	}

	slog.Info("DNS sync operations",
		"toUpsert", len(upsertHostnames), "upsertList", upsertHostnames,
		"toDelete", len(deleteHostnames), "deleteList", deleteHostnames)

	// 执行批量删除操作
	if len(deleteHostnames) > 0 {
		slog.Debug("Deleting DNS records", "hostnames", deleteHostnames)
		if err := c.cloudflareManager.DeleteDNSRecords(ctx, deleteHostnames); err != nil {
			slog.Error("Failed to batch delete DNS records", "error", err)
			return err
		}
		slog.Info("Batch deleted DNS records", "count", len(deleteHostnames))
	}

	// 执行批量创建/更新操作
	if len(upsertHostnames) > 0 {
		if err := c.cloudflareManager.UpsertDNSRecords(ctx, upsertHostnames); err != nil {
			slog.Error("Failed to batch upsert DNS records", "error", err)
			return err
		}
		slog.Info("Batch upserted DNS records", "count", len(upsertHostnames))
	}

	slog.Info("Finished syncing DNS records")

	return nil
}

// GetIngressRules 获取当前的Ingress规则
func (c *Controller) GetIngressRules() []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 创建规则切片
	rules := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(c.ingressRules)+1)

	// 添加所有规则
	for _, rule := range c.ingressRules {
		rules = append(rules, rule)
	}

	// 总是添加catch-all规则作为最后一个规则
	rules = append(rules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http_status:404"),
	})

	return rules
}

// isFlapping 检查容器是否处于抖动状态
func (c *Controller) isFlapping(containerID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	health, exists := c.containerHealth[containerID]
	if !exists {
		return false
	}

	// 检查是否在冷却期内
	if health.IsFlapping && time.Now().Before(health.CoolingUntil) {
		return true
	}

	return false
}

// updateContainerHealth 更新容器健康状态
func (c *Controller) updateContainerHealth(containerID string, isStartEvent bool) {
	now := time.Now()

	health, exists := c.containerHealth[containerID]
	if !exists {
		health = &ContainerHealth{}
		c.containerHealth[containerID] = health
	}

	if isStartEvent {
		// 如果是启动事件，检查是否在窗口期内
		if now.Sub(health.LastRestart) <= c.flappingWindow {
			health.RestartCount++

			// 如果重启次数超过阈值，标记为抖动状态
			if health.RestartCount >= c.flappingThreshold {
				health.IsFlapping = true
				// 计算冷却期（指数退避，但不超过最大冷却期）
				coolingMultiplier := 1 << uint(health.RestartCount-c.flappingThreshold)
				coolingDuration := time.Duration(coolingMultiplier) * c.coolingPeriod
				if coolingDuration > c.maxCoolingPeriod {
					coolingDuration = c.maxCoolingPeriod
				}
				health.CoolingUntil = now.Add(coolingDuration)

				slog.Warn("Container marked as flapping",
					"containerID", containerID,
					"restartCount", health.RestartCount,
					"coolingUntil", health.CoolingUntil)
			}
		} else {
			// 重置重启计数
			health.RestartCount = 1
		}

		health.LastRestart = now
	} else {
		// 如果是停止事件，不更新重启计数，但可以记录日志
		slog.Debug("Container stopped", "containerID", containerID)
	}

	// 检查是否已经过了冷却期
	if health.IsFlapping && now.After(health.CoolingUntil) {
		health.IsFlapping = false
		health.RestartCount = 0
		slog.Info("Container cooling period ended, flapping status reset", "containerID", containerID)
	}
}

// RunGarbageCollection runs garbage collection for expired retention policies (T062, T063)
// This should be called periodically (every 60 seconds) to clean up expired entries
func (c *Controller) RunGarbageCollection(ctx context.Context) error {
	// Run GC on state manager — returns expired entries with hostname info
	expiredEntries, err := c.stateManager.RunGC(ctx)
	if err != nil {
		return fmt.Errorf("state manager GC failed: %w", err)
	}

	// If no expired entries, return early
	if len(expiredEntries) == 0 {
		return nil
	}

	slog.Info("Garbage collection found expired entries", "count", len(expiredEntries))

	// Remove expired entries from ingress rules
	c.mu.Lock()
	for _, entry := range expiredEntries {
		hostname := entry.Config.Hostname
		if hostname != "" {
			if _, exists := c.ingressRules[hostname]; exists {
				delete(c.ingressRules, hostname)
				slog.Info("Removed expired route from ingress rules",
					"containerID", entry.ContainerID,
					"hostname", hostname)
			}
		}
	}
	c.mu.Unlock()

	// Sync updated rules to Cloudflare (outside lock to avoid deadlock)
	if err := c.syncToCloudflare(ctx); err != nil {
		return fmt.Errorf("failed to sync after GC: %w", err)
	}

	slog.Info("Garbage collection completed successfully",
		"expired_count", len(expiredEntries))

	return nil
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
			"error", err, "containerID", event.ContainerID)
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
	slog.Info("Handling resync event after Docker reconnection")
	return c.Sync(ctx)
}

// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
func (c *Controller) Reconcile(ctx context.Context) error {
	if c.dockerManager == nil {
		return nil
	}

	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	// 1. Scan running containers → build desired active rules
	eventsList, err := c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}

	desiredRules := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	desiredContainerRules := make(map[string][]string)

	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse labels during reconcile",
				"containerID", event.ContainerID, "error", err)
			continue
		}

		var hostnames []string
		for _, rule := range parsedRules {
			if rule.Hostname.Value != "" {
				desiredRules[rule.Hostname.Value] = *rule
				hostnames = append(hostnames, rule.Hostname.Value)
			}
		}
		if len(hostnames) > 0 {
			desiredContainerRules[event.ContainerID] = hostnames
		}
	}

	// 2. Preserve retaining rules (in ingressRules but not in containerRules)
	c.mu.RLock()
	currentContainerHostnames := make(map[string]bool)
	for _, hostnames := range c.containerRules {
		for _, h := range hostnames {
			currentContainerHostnames[h] = true
		}
	}
	for hostname, rule := range c.ingressRules {
		if !currentContainerHostnames[hostname] {
			if _, inDesired := desiredRules[hostname]; !inDesired {
				desiredRules[hostname] = rule
			}
		}
	}
	c.mu.RUnlock()

	// 3. Diff desired vs current
	c.mu.RLock()
	hasDiff := len(desiredRules) != len(c.ingressRules)
	if !hasDiff {
		for hostname, desiredRule := range desiredRules {
			existing, exists := c.ingressRules[hostname]
			if !exists || existing.Service.Value != desiredRule.Service.Value {
				hasDiff = true
				break
			}
		}
	}
	c.mu.RUnlock()

	if !hasDiff {
		slog.Debug("Reconcile: no drift detected")
		return nil
	}

	slog.Info("Reconcile: drift detected, syncing",
		"current_rules", len(c.ingressRules),
		"desired_rules", len(desiredRules))

	// 4. Apply desired state
	c.mu.Lock()
	c.ingressRules = desiredRules
	c.containerRules = desiredContainerRules
	c.mu.Unlock()

	if err := c.syncToCloudflare(ctx); err != nil {
		return err
	}

	c.refreshActualState(ctx)

	return nil
}

// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (c *Controller) ReconcileEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reconcileEnabled
}

// ReconcileInterval returns the reconciliation interval.
func (c *Controller) ReconcileInterval() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reconcileInterval
}

// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
// Safe to call from any goroutine. Uses cached state — never makes Cloudflare API calls.
func (c *Controller) GetDebugState() diagnostics.DebugStateResponse {
	c.actualStateMu.RLock()
	defer c.actualStateMu.RUnlock()

	desired := c.snapshotRuleViewsLocked()
	actual := c.lastKnownActualRules
	source := "empty"
	if actual != nil {
		source = "live_cache"
	}

	return diagnostics.DebugStateResponse{
		Timestamp:    time.Now(),
		DesiredState: desired,
		ActualState:  actual,
		Source:       source,
		Diff:         diagnostics.ComputeDiff(desired, actual),
	}
}

// refreshActualState fetches the live tunnel config from Cloudflare and
// caches it for /debug/state. Safe to call from Sync/Reconcile.
func (c *Controller) refreshActualState(ctx context.Context) {
	ingress, err := c.cloudflareManager.GetConfiguration(ctx)
	if err != nil {
		slog.Warn("Failed to fetch live tunnel config for diagnostics",
			"error", err)
		return
	}

	views := make([]diagnostics.RuleView, 0, len(ingress))
	for _, rule := range ingress {
		if rule.Hostname == "" {
			continue // skip catch-all rules like http_status:404
		}
		views = append(views, diagnostics.RuleView{
			Hostname: rule.Hostname,
			Service:  rule.Service,
			Path:     rule.Path,
		})
	}
	c.setLastKnownActualRules(views)
}

// setLastKnownActualRules replaces the cached actual-state snapshot.
// Called by Sync/Reconcile after a successful UpdateConfiguration push.
func (c *Controller) setLastKnownActualRules(rules []diagnostics.RuleView) {
	c.actualStateMu.Lock()
	c.lastKnownActualRules = rules
	c.actualStateMu.Unlock()
}

// snapshotRuleViewsLocked builds a slice of RuleView from c.ingressRules.
// Caller must hold c.mu (read or write).
func (c *Controller) snapshotRuleViewsLocked() []diagnostics.RuleView {
	views := make([]diagnostics.RuleView, 0, len(c.ingressRules))
	for _, rule := range c.ingressRules {
		views = append(views, diagnostics.RuleView{
			Hostname: rule.Hostname.Value,
			Service:  rule.Service.Value,
			Path:     rule.Path.Value,
		})
	}
	return views
}
