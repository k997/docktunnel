package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	eventTypes "github.com/docker/docker/api/types/events"

	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/docker"
	"docktunnel/internal/events"
)

// Controller 负责协调Docker和Cloudflare模块的工作
type Controller struct {
	dockerManager     *docker.Manager
	cloudflareManager *cloudflareManager.Manager
	ingressRules      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress // hostname -> rule map
	containerRules    map[string][]string                                                           // containerID -> hostnames map
	ruleValidator     RuleValidator
	mu                sync.RWMutex
}

// NewController 创建一个新的控制器实例
func NewController(dockerManager *docker.Manager, cloudflareManager *cloudflareManager.Manager, catchAllService string) *Controller {
	controller := &Controller{
		dockerManager:     dockerManager,
		cloudflareManager: cloudflareManager,
		ingressRules:      make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules:    make(map[string][]string),
		ruleValidator:     NewCompositeValidator(),
	}

	// 初始化默认的catch-all规则
	catchAllRule := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F(catchAllService),
	}
	controller.ingressRules["CATCH_ALL"] = catchAllRule

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
	// 保存现有的catch-all规则
	catchAllRule, catchAllExists := c.ingressRules["CATCH_ALL"]

	// 清空ingressRules和containerRules
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	c.containerRules = make(map[string][]string)

	// 恢复catch-all规则
	if catchAllExists {
		c.ingressRules["CATCH_ALL"] = catchAllRule
	}
	c.mu.Unlock()

	// 调用syncToCloudflare同步空的规则集（这将删除所有DNS记录）
	if err := c.syncToCloudflare(ctx); err != nil {
		return err
	}

	// 注意：我们不删除tunnel本身，因为这可能会影响其他服务
	// 如果需要删除tunnel，用户可以手动删除或通过Cloudflare仪表板操作

	return nil
}

// handleContainerStart 处理容器启动事件
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// 检查是否有容器信息
	if event.ContainerInfo == nil {
		slog.Warn("Container info is nil for start event", "containerID", event.ContainerID)
		return nil
	}

	// 检查容器是否启用了docktunnel
	if event.ContainerInfo.Config == nil || event.ContainerInfo.Config.Labels == nil {
		return nil
	}

	if event.ContainerInfo.Config.Labels["docktunnel.enable"] != "true" {
		return nil
	}

	// 解析容器标签生成规则
	ingressRules, err := parseLabelsToIngress(event.ContainerInfo, c.ruleValidator)
	if err != nil {
		slog.Error("Failed to parse container labels", "error", err)
		return fmt.Errorf("failed to parse container labels: %w", err)
	}

	// 收集主机名列表
	hostnames := make([]string, 0)
	for _, rule := range ingressRules {
		// 跳过没有主机名的规则（catch-all规则）
		if rule.Hostname.Value != "" {
			hostnames = append(hostnames, rule.Hostname.Value)
		}
	}

	// 更新内部状态
	c.mu.Lock()
	// 添加规则
	for _, rule := range ingressRules {
		// 跳过没有主机名的规则（catch-all规则）
		if rule.Hostname.Value == "" {
			continue
		}
		c.ingressRules[rule.Hostname.Value] = rule
	}

	// 记录容器与主机名的关联关系
	c.containerRules[event.ContainerID] = hostnames
	c.mu.Unlock()

	// 同步到Cloudflare
	return c.syncToCloudflare(ctx)
}

// handleContainerStop 处理容器停止事件
func (c *Controller) handleContainerStop(ctx context.Context, event events.Event) error {
	slog.Info("Handling container stop event", "containerID", event.ContainerID)

	// 获取要删除的规则
	c.mu.Lock()
	hostnamesToRemove, exists := c.containerRules[event.ContainerID]
	if !exists {
		c.mu.Unlock()
		slog.Warn("No rules found for stopped container", "containerID", event.ContainerID)
		return nil
	}

	// 从容器规则映射中删除
	delete(c.containerRules, event.ContainerID)

	// 从ingress规则中删除对应的规则
	for _, hostname := range hostnamesToRemove {
		delete(c.ingressRules, hostname)
	}
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

	// 收集所有ingress规则
	var allIngressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	containerHostnames := make(map[string][]string) // containerID -> hostnames

	for _, event := range eventsList {
		if event.ContainerInfo != nil &&
			event.ContainerInfo.Config != nil &&
			event.ContainerInfo.Config.Labels != nil {

			// 解析标签获取主机名
			ingressRules, err := parseLabelsToIngress(event.ContainerInfo, c.ruleValidator)
			if err != nil {
				slog.Error("Failed to parse container labels during sync", "containerID", event.ContainerID, "error", err)
				continue
			}

			// 收集规则
			allIngressRules = append(allIngressRules, ingressRules...)

			// 收集主机名
			hostnames := make([]string, 0)
			for _, rule := range ingressRules {
				// 跳过没有主机名的规则（catch-all规则）
				if rule.Hostname.Value != "" {
					hostnames = append(hostnames, rule.Hostname.Value)
				}
			}
			containerHostnames[event.ContainerID] = hostnames
		}
	}

	// 更新内部状态
	c.mu.Lock()
	// 保存现有的catch-all规则
	catchAllRule, catchAllExists := c.ingressRules["CATCH_ALL"]

	// 重新创建ingress规则map
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)

	// 添加所有新规则
	for _, rule := range allIngressRules {
		// 跳过没有主机名的规则（catch-all规则）
		if rule.Hostname.Value == "" {
			continue
		}
		c.ingressRules[rule.Hostname.Value] = rule
	}

	// 恢复catch-all规则
	if catchAllExists {
		c.ingressRules["CATCH_ALL"] = catchAllRule
	}

	// 更新容器与主机名的关联关系
	c.containerRules = containerHostnames
	c.mu.Unlock()

	// 同步到Cloudflare
	return c.syncToCloudflare(ctx)
}

// syncToCloudflare 将当前规则同步到Cloudflare
func (c *Controller) syncToCloudflare(ctx context.Context) error {
	// 构建规则列表
	ingressRules := c.GetIngressRules()

	// 从cloudflareManager获取tunnel信息
	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Using tunnel", "tunnelID", tunnel.ID)

	// 更新配置（保持原始的ingress规则，不需要修改Service字段）
	if err := c.cloudflareManager.UpdateConfiguration(ctx, ingressRules); err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "ruleCount", len(ingressRules)-1) // -1 for catch-all rule

	// 同步DNS记录
	if err := c.syncDNSRecords(ctx); err != nil {
		return fmt.Errorf("failed to sync DNS records: %w", err)
	}

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

	slog.Info("Syncing DNS records", "hostnamesCount", len(currentHostnames))

	// 先获取Cloudflare上当前的所有DNS记录，以减少API访问次数
	allTunnelRecords, err := c.cloudflareManager.ListDNSRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tunnel DNS records: %w", err)
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

	// 创建规则切片，为所有规则加上catch-all规则预留空间
	rules := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(c.ingressRules))

	// 添加所有非catch-all规则
	for hostname, rule := range c.ingressRules {
		// 跳过catch-all规则，稍后专门添加
		if hostname != "CATCH_ALL" {
			rules = append(rules, rule)
		}
	}

	// 添加catch-all规则
	if catchAllRule, exists := c.ingressRules["CATCH_ALL"]; exists {
		rules = append(rules, catchAllRule)
	} else {
		// 如果由于某种原因catch-all规则不存在，则使用默认值
		rules = append(rules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Service: cloudflare.F("http_status:404"),
		})
	}

	return rules
}
