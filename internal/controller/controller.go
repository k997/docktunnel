package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cloudflare/cloudflare-go"
	eventTypes "github.com/docker/docker/api/types/events"

	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/docker"
	"docktunnel/internal/events"
)

// Controller 负责协调Docker和Cloudflare模块的工作
type Controller struct {
	dockerManager     *docker.Manager
	cloudflareManager *cloudflareManager.Manager
	ingressRules      map[string]cloudflare.UnvalidatedIngressRule // hostname -> rule map
	containerRules    map[string][]string                          // containerID -> hostnames map
	ruleValidator     RuleValidator
	mu                sync.RWMutex
}

// NewController 创建一个新的控制器实例
func NewController(dockerManager *docker.Manager, cloudflareManager *cloudflareManager.Manager, catchAllService string) *Controller {
	controller := &Controller{
		dockerManager:     dockerManager,
		cloudflareManager: cloudflareManager,
		ingressRules:      make(map[string]cloudflare.UnvalidatedIngressRule),
		containerRules:    make(map[string][]string),
		ruleValidator:     NewCompositeValidator(),
	}

	// 其他初始化逻辑...

	// 初始化默认的catch-all规则
	catchAllRule := cloudflare.UnvalidatedIngressRule{
		Service: catchAllService,
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
		if rule.Hostname != "" {
			hostnames = append(hostnames, rule.Hostname)
		}
	}

	// 更新内部状态
	c.mu.Lock()
	// 添加规则
	for _, rule := range ingressRules {
		// 跳过没有主机名的规则（catch-all规则）
		if rule.Hostname == "" {
			continue
		}
		c.ingressRules[rule.Hostname] = rule
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
	if err := c.syncToCloudflare(ctx); err != nil {
		return err
	}

	// 删除已停止容器对应的DNS记录
	for _, hostname := range hostnamesToRemove {
		if err := c.cloudflareManager.DeleteDNSRecord(ctx, hostname); err != nil {
			slog.Error("Failed to delete DNS record", "hostname", hostname, "error", err)
		} else {
			slog.Info("Deleted DNS record", "hostname", hostname)
		}
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
	var allIngressRules []cloudflare.UnvalidatedIngressRule
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
				if rule.Hostname != "" {
					hostnames = append(hostnames, rule.Hostname)
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
	c.ingressRules = make(map[string]cloudflare.UnvalidatedIngressRule)
	
	// 添加所有新规则
	for _, rule := range allIngressRules {
		// 跳过没有主机名的规则（catch-all规则）
		if rule.Hostname == "" {
			continue
		}
		c.ingressRules[rule.Hostname] = rule
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

	// 更新配置
	if err := c.cloudflareManager.UpdateConfiguration(ctx, ingressRules); err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "ruleCount", len(ingressRules)-1) // -1 for catch-all rule

	// 为每个主机名创建或更新DNS记录
	for _, rule := range ingressRules {
		// 只为有主机名的规则创建DNS记录（跳过catch-all规则）
		if rule.Hostname != "" {
			if err := c.cloudflareManager.UpsertDNSRecord(ctx, rule.Hostname, tunnel.ID); err != nil {
				slog.Error("Failed to upsert DNS record", "hostname", rule.Hostname, "error", err)
				// 继续处理其他主机名，不因单个错误而中断整个过程
			} else {
				slog.Info("Upserted DNS record", "hostname", rule.Hostname)
			}
		}
	}

	return nil
}

// GetIngressRules 获取当前的Ingress规则
func (c *Controller) GetIngressRules() []cloudflare.UnvalidatedIngressRule {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 创建规则切片，为所有规则加上catch-all规则预留空间
	rules := make([]cloudflare.UnvalidatedIngressRule, 0, len(c.ingressRules))

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
		rules = append(rules, cloudflare.UnvalidatedIngressRule{
			Service: "http_status:404",
		})
	}
	
	return rules
}
