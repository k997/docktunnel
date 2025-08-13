package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// 解析容器标签生成规则
	ingressRules, err := c.parseLabelsToIngress(containers)
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

// parseLabelsToIngress 解析容器标签并生成Ingress规则
func (c *Controller) parseLabelsToIngress(containers []docker.Container) ([]cloudflare.UnvalidatedIngressRule, error) {
	// 创建临时存储，键是服务名称，值是该服务对应的Ingress规则
	rawRules := make(map[string]*cloudflare.UnvalidatedIngressRule)
	
	// 遍历所有容器，解析其Labels
	for _, container := range containers {
		for label, value := range container.Labels {
			// 检查是否是docktunnel标签
			if !strings.HasPrefix(label, "docktunnel.") {
				continue
			}
			
			// 解析标签格式: docktunnel.<service-name>.<attribute>
			parts := strings.Split(label, ".")
			if len(parts) < 3 {
				// 特殊处理docktunnel.enable标签，它只有两部分
				if len(parts) == 2 && parts[1] == "enable" {
					// 这是启用标签，不需要特殊处理，继续解析其他标签
					continue
				}
				slog.Warn("Invalid label format, skipping", "label", label, "containerID", container.ID[:12])
				continue
			}
			
			serviceName := parts[1]
			attribute := strings.Join(parts[2:], ".") // 处理多级属性如 originRequest.noTLSVerify
			
			// 获取或创建该服务的规则
			rule, exists := rawRules[serviceName]
			if !exists {
				rule = &cloudflare.UnvalidatedIngressRule{}
			}
			
			// 根据属性设置规则字段
			switch attribute {
			case "hostname":
				rule.Hostname = value
			case "service":
				rule.Service = value
			case "path":
				rule.Path = value
			// 源站请求配置
			case "originRequest.noTLSVerify":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				if value == "true" {
					noTLSVerify := true
					rule.OriginRequest.NoTLSVerify = &noTLSVerify
				} else if value == "false" {
					noTLSVerify := false
					rule.OriginRequest.NoTLSVerify = &noTLSVerify
				}
			case "originRequest.connectTimeout":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				// 解析时间值
				if duration, err := time.ParseDuration(value); err == nil {
					tunnelDuration := cloudflare.TunnelDuration{Duration: duration}
					rule.OriginRequest.ConnectTimeout = &tunnelDuration
				} else {
					slog.Warn("Invalid connect timeout value, skipping", "value", value, "containerID", container.ID[:12])
				}
			case "originRequest.tlsTimeout":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				// 解析时间值
				if duration, err := time.ParseDuration(value); err == nil {
					tunnelDuration := cloudflare.TunnelDuration{Duration: duration}
					rule.OriginRequest.TLSTimeout = &tunnelDuration
				} else {
					slog.Warn("Invalid TLS timeout value, skipping", "value", value, "containerID", container.ID[:12])
				}
			case "originRequest.keepAliveConnections":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				if num, err := strconv.Atoi(value); err == nil {
					rule.OriginRequest.KeepAliveConnections = &num
				} else {
					slog.Warn("Invalid keep alive connections value, skipping", "value", value, "containerID", container.ID[:12])
				}
			case "originRequest.keepAliveTimeout":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				// 解析时间值
				if duration, err := time.ParseDuration(value); err == nil {
					tunnelDuration := cloudflare.TunnelDuration{Duration: duration}
					rule.OriginRequest.KeepAliveTimeout = &tunnelDuration
				} else {
					slog.Warn("Invalid keep alive timeout value, skipping", "value", value, "containerID", container.ID[:12])
				}
			case "originRequest.http2Origin":
				if rule.OriginRequest == nil {
					rule.OriginRequest = &cloudflare.OriginRequestConfig{}
				}
				
				if value == "true" {
					http2Origin := true
					rule.OriginRequest.Http2Origin = &http2Origin
				} else if value == "false" {
					http2Origin := false
					rule.OriginRequest.Http2Origin = &http2Origin
				}
			}
			
			// 更新规则
			rawRules[serviceName] = rule
		}
	}
	
	// 验证规则
	if err := c.ruleValidator.Validate(rawRules); err != nil {
		return nil, err
	}
	
	// 转换为Cloudflare Ingress规则列表
	var ingressRules []cloudflare.UnvalidatedIngressRule
	for _, rule := range rawRules {
		ingressRules = append(ingressRules, *rule)
	}
	
	// 添加默认的catch-all规则
	ingressRules = append(ingressRules, cloudflare.UnvalidatedIngressRule{
		Service: "http_status:404",
	})
	
	return ingressRules, nil
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