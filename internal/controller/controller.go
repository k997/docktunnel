package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cloudflare/cloudflare-go"

	"docktunnel/internal/docker"
	"docktunnel/internal/cloudflareManager"
)

// Controller 负责协调Docker和Cloudflare模块的工作
type Controller struct {
	dockerManager     *docker.Manager
	cloudflareManager *cloudflareManager.Manager
	tunnelName        string
	ingressRules      []cloudflare.UnvalidatedIngressRule
	ruleValidator     RuleValidator
	mu                sync.RWMutex
}

// NewController 创建一个新的控制器实例
func NewController(dockerManager *docker.Manager, cloudflareManager *cloudflareManager.Manager, tunnelName string) *Controller {
	return &Controller{
		dockerManager:     dockerManager,
		cloudflareManager: cloudflareManager,
		tunnelName:        tunnelName,
		ingressRules:      make([]cloudflare.UnvalidatedIngressRule, 0),
		ruleValidator:     NewCompositeValidator(),
	}
}

// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (c *Controller) Sync(ctx context.Context) error {
	slog.Info("Starting synchronization", "tunnelName", c.tunnelName)

	// 扫描运行中的容器
	containers, err := c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to scan running containers: %w", err)
	}

	slog.Info("Found containers with docktunnel labels", "count", len(containers))

	// 将所有容器的标签合并为一个map
	allLabels := make(map[string]string)
	for _, container := range containers {
		for label, value := range container.Labels {
			allLabels[label] = value
		}
	}

	// 解析容器标签生成规则
	ingressRules, err := parseLabelsToIngress(allLabels, c.ruleValidator)
	if err != nil {
		return fmt.Errorf("failed to parse container labels: %w", err)
	}

	// 更新Cloudflare Tunnel配置和DNS记录
	if err := c.updateCloudflareConfiguration(ctx, ingressRules, containers); err != nil {
		return fmt.Errorf("failed to update cloudflare configuration: %w", err)
	}

	slog.Info("Synchronization completed successfully")
	return nil
}


// updateCloudflareConfiguration 更新Cloudflare Tunnel配置和DNS记录
func (c *Controller) updateCloudflareConfiguration(ctx context.Context, ingressRules []cloudflare.UnvalidatedIngressRule, containers []docker.Container) error {
	// 获取或创建Tunnel
	tunnelID, err := c.cloudflareManager.GetOrCreateTunnel(ctx, c.tunnelName)
	if err != nil {
		return fmt.Errorf("failed to get or create tunnel: %w", err)
	}

	slog.Info("Using tunnel", "tunnelID", tunnelID, "tunnelName", c.tunnelName)

	// 更新配置
	if err := c.cloudflareManager.UpdateConfiguration(ctx, ingressRules); err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "ruleCount", len(ingressRules)-1) // -1 for catch-all rule

	// 收集所有需要的主机名
	hostnames := make(map[string]bool)
	for _, rule := range ingressRules {
		if rule.Hostname != "" && rule.Service != "http_status:404" {
			hostnames[rule.Hostname] = true
		}
	}

	// 为每个主机名创建或更新DNS记录
	for hostname := range hostnames {
		if err := c.cloudflareManager.UpsertDNSRecord(ctx, hostname, tunnelID); err != nil {
			slog.Error("Failed to upsert DNS record", "hostname", hostname, "error", err)
			// 继续处理其他主机名，不因单个错误而中断整个过程
		} else {
			slog.Info("Upserted DNS record", "hostname", hostname)
		}
	}

	// 保存规则到控制器状态
	c.mu.Lock()
	c.ingressRules = ingressRules
	c.mu.Unlock()

	return nil
}

// GetIngressRules 获取当前的Ingress规则
func (c *Controller) GetIngressRules() []cloudflare.UnvalidatedIngressRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	rules := make([]cloudflare.UnvalidatedIngressRule, len(c.ingressRules))
	copy(rules, c.ingressRules)
	return rules
}