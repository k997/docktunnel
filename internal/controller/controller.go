package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	eventTypes "github.com/docker/docker/api/types/events"

	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/docker"
	"docktunnel/internal/events"
)

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
	cloudflareManager *cloudflareManager.Manager
	stateManager      *state.Manager // State manager for retention policies
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
}

// NewController 创建一个新的控制器实例
func NewController(dockerManager *docker.Manager, cloudflareManager *cloudflareManager.Manager, opts ControllerOptions) *Controller {
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

	return controller
}

// Dispatch 是所有事件处理的统一入口
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
	switch event.Type {
	case eventTypes.ActionStart:
		return c.handleContainerStart(ctx, event)
	case eventTypes.ActionStop:
		return c.handleContainerStop(ctx, event)
	case eventTypes.ActionDie:
		return c.handleContainerStop(ctx, event)
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

// handleContainerStart 处理容器启动事件
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	// 检查容器是否启用了docktunnel
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// Check if container is restarting from pending deletion (T064)
	if _, pending := c.stateManager.GetPendingDeletion(event.ContainerID); pending {
		slog.Info("Container restarting during retention period, canceling retention timer",
			"containerID", event.ContainerID)

		// Restore to active state
		if err := c.stateManager.RestoreActiveTunnel(event.ContainerID); err != nil {
			slog.Warn("Failed to restore active tunnel for container",
				"containerID", event.ContainerID,
				"error", err)
		}

		// Container will continue through normal start flow
		// to recreate containerRules and ingressRules entries
	}

	// 检查容器是否处于抖动状态
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	// 解析容器标签生成规则
	parsedRules, err := parseLabelsToIngress(event.ContainerInfo)
	if err != nil {
		slog.Error("Failed to parse container labels", "error", err, "containerID", event.ContainerID)
		return fmt.Errorf("failed to parse container labels for container %s: %w", event.ContainerID, err)
	}

	// 验证规则
	c.mu.RLock()
	if err := c.ruleValidator.Validate(parsedRules, c.ingressRules); err != nil {
		c.mu.RUnlock()
		slog.Error("Invalid ingress rules", "error", err, "containerID", event.ContainerID)
		return fmt.Errorf("invalid ingress rules for container %s: %w", event.ContainerID, err)
	}
	c.mu.RUnlock()

	// 收集主机名列表
	hostnames := make([]string, 0)
	for _, rule := range parsedRules {
		// 收集所有主机名
		if rule.Hostname.Value != "" {
			hostnames = append(hostnames, rule.Hostname.Value)
		}
	}

	// 更新内部状态
	c.mu.Lock()
	// 添加规则
	for _, rule := range parsedRules {
		// 添加所有有主机名的规则
		if rule.Hostname.Value != "" {
			c.ingressRules[rule.Hostname.Value] = *rule
		}
	}

	// 记录容器与主机名的关联关系
	c.containerRules[event.ContainerID] = hostnames

	// 更新容器健康状态
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()

	// 同步到Cloudflare
	return c.syncToCloudflare(ctx)
}

// getContainerRetentionPolicy parses the retention policy from container labels
// Defaults to Immediate deletion if not specified
func (c *Controller) getContainerRetentionPolicy(event events.Event) types.RetentionPolicy {
	labels := event.ContainerInfo.Config.Labels

	// Check for retention label
	retentionLabel, exists := labels["docktunnel.retention"]
	if !exists {
		// Try per-service retention labels (use first service)
		for key, value := range labels {
			if strings.HasPrefix(key, "docktunnel.") && strings.HasSuffix(key, ".retention") {
				retentionLabel = value
				exists = true
				break
			}
		}
	}

	if !exists {
		// Default to immediate deletion
		return types.RetentionPolicy{Type: types.Immediate}
	}

	policy, err := ParseRetentionPolicy(retentionLabel)
	if err != nil {
		slog.Warn("Invalid retention policy, defaulting to immediate",
			"containerID", event.ContainerID,
			"retention", retentionLabel,
			"error", err)
		return types.RetentionPolicy{Type: types.Immediate}
	}

	return policy
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

// handleContainerStop 处理容器停止事件
func (c *Controller) handleContainerStop(ctx context.Context, event events.Event) error {
	// 检查容器是否启用了docktunnel
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container stop event", "containerID", event.ContainerID)

	// 检查容器是否处于抖动状态
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring stop event", "containerID", event.ContainerID)
		return nil
	}

	// 获取要删除的规则
	c.mu.Lock()
	hostnamesToRemove, exists := c.containerRules[event.ContainerID]
	if !exists {
		c.mu.Unlock()
		slog.Warn("No rules found for stopped container", "containerID", event.ContainerID)
		return nil
	}

	// Get retention policy
	policy := c.getContainerRetentionPolicy(event)
	slog.Info("Container retention policy",
		"containerID", event.ContainerID,
		"policyType", policy.Type,
		"duration", policy.Duration)

	now := time.Now()

	// Handle based on retention policy
	if policy.Type == types.Immediate {
		// Immediate deletion - existing behavior
		slog.Info("Immediate deletion for container", "containerID", event.ContainerID)

		// 从容器规则映射中删除
		delete(c.containerRules, event.ContainerID)

		// 从ingress规则中删除对应的规则
		for _, hostname := range hostnamesToRemove {
			delete(c.ingressRules, hostname)
		}
	} else {
		// Timed or Forever - move to pending deletions
		slog.Info("Moving container to pending deletions",
			"containerID", event.ContainerID,
			"policy", policy.Type,
			"duration", policy.Duration)

		// Create tunnel entries for state manager
		parsedRules, err := parseLabelsToIngress(event.ContainerInfo)
		if err != nil {
			c.mu.Unlock()
			return fmt.Errorf("failed to parse labels for pending deletion: %w", err)
		}

		// Add each service to state manager with pending deletion status
		for serviceName, rule := range parsedRules {
			entry := &types.TunnelEntry{
				ContainerID: event.ContainerID,
				TunnelID:    c.cloudflareManager.GetTunnel().ID,
				ServiceName: serviceName,
				Config: types.TunnelConfiguration{
					Hostname:  rule.Hostname.Value,
					ServiceURL: rule.Service.Value,
				},
				RetentionPolicy: policy,
				Status:          types.StatusPendingDelete,
				CreatedAt:       now, // Should use actual creation time, but using now for simplicity
				DeletedAt:       &now,
				LastSyncAt:      now,
			}
			c.stateManager.AddPendingDeletion(entry)
		}

		// Remove from containerRules but keep in ingressRules (route still active)
		delete(c.containerRules, event.ContainerID)
		slog.Info("Routes kept active during retention period",
			"containerID", event.ContainerID,
			"hostnames", hostnamesToRemove,
			"retention", policy.Duration)
	}

	// 更新容器健康状态
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

	// 同步更新后的规则到Cloudflare
	// syncToCloudflare会调用syncDNSRecords来处理DNS记录的同步（包括删除不再需要的记录）
	if err := c.syncToCloudflare(ctx); err != nil {
		return err
	}

	return nil
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
		parsedRules, err := parseLabelsToIngress(event.ContainerInfo)
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
	return c.syncToCloudflare(ctx)
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
	// 收集当前需要的主机名（从containerRules中获取）
	currentHostnames := make(map[string]bool)
	for _, hostnames := range c.containerRules {
		for _, hostname := range hostnames {
			currentHostnames[hostname] = true
		}
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
	// Run GC on state manager
	expiredContainers, err := c.stateManager.RunGC(ctx)
	if err != nil {
		return fmt.Errorf("state manager GC failed: %w", err)
	}

	// If no expired containers, return early
	if len(expiredContainers) == 0 {
		return nil
	}

	slog.Info("Garbage collection found expired containers", "count", len(expiredContainers))

	// Remove expired entries from ingress rules and sync to Cloudflare
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, containerID := range expiredContainers {
		slog.Info("Removing expired retention entries",
			"containerID", containerID)

		// Get all pending deletions for this container to find hostnames
		pendingEntry, exists := c.stateManager.GetPendingDeletion(containerID)
		if !exists {
			// Already removed from state manager, skip
			continue
		}

		// Remove from ingress rules using hostname from config
		hostname := pendingEntry.Config.Hostname
		if hostname != "" {
			if _, exists := c.ingressRules[hostname]; exists {
				delete(c.ingressRules, hostname)
				slog.Info("Removed expired route from ingress rules",
					"containerID", containerID,
					"hostname", hostname)
			}
		}

		// Remove from state manager pending deletions (should already be removed by RunGC)
		c.stateManager.RemovePendingDeletion(containerID)
	}

	// Sync updated rules to Cloudflare
	if err := c.syncToCloudflare(ctx); err != nil {
		return fmt.Errorf("failed to sync after GC: %w", err)
	}

	slog.Info("Garbage collection completed successfully",
		"expired_count", len(expiredContainers))

	return nil
}
