package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// registerContainerRules parses labels, validates, and registers ingress rules for a container.
// Returns the parsed rules (serviceName → rule) for per-service state management.
func (c *Controller) registerContainerRules(ctx context.Context, event events.Event) (map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
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

	var hostnames []string
	seen := make(map[string]bool)
	c.mu.Lock()
	for _, rule := range parsedRules {
		if rule.Hostname.Value == "" {
			continue
		}
		// Key is (hostname, path) since B10, so one hostname may expose
		// multiple paths.
		setIngressRuleLocked(c, *rule)
		if !seen[rule.Hostname.Value] {
			seen[rule.Hostname.Value] = true
			hostnames = append(hostnames, rule.Hostname.Value)
		}
	}
	// Update containerRules for aggregate lookup
	c.containerRules[event.ContainerID] = hostnames
	c.mu.Unlock()

	return parsedRules, nil
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

	parsedRules, err := c.registerContainerRules(ctx, event)
	if err != nil {
		slog.Error("Failed to register container rules",
			"action", "register_rules",
			"result", "failure",
			"error", err,
			"containerID", event.ContainerID)
		return err
	}

	// Create one TunnelEntry per service with per-service retention policy.
	// The full ingress rule is persisted as RuleJSON so retention can survive
	// a process restart (B1).
	labels := event.ContainerInfo.Config.Labels
	now := time.Now()
	for serviceName, rule := range parsedRules {
		if rule.Hostname.Value == "" {
			continue
		}
		policy := c.getServiceRetentionPolicy(labels, serviceName)
		ruleJSON, marshalErr := marshalIngressRule(rule)
		if marshalErr != nil {
			slog.Warn("Failed to serialize ingress rule for state persistence",
				"containerID", event.ContainerID,
				"service", serviceName,
				"error", marshalErr)
		}
		tunnelEntry := &types.TunnelEntry{
			ContainerID:     event.ContainerID,
			ServiceName:     serviceName,
			RetentionPolicy: policy,
			Status:          types.StatusActive,
			CreatedAt:       now,
			LastSyncAt:      now,
			Config: types.TunnelConfiguration{
				Hostname: rule.Hostname.Value,
				RuleJSON: ruleJSON,
			},
		}
		c.stateManager.AddActiveTunnel(tunnelEntry)
	}

	c.updateContainerHealth(event.ContainerID, true)

	return c.syncToCloudflare(ctx)
}

// getServiceRetentionPolicy parses the retention policy for a specific service.
// Priority: per-service delete_retention/retention > global
// delete_retention/retention > Immediate. Both key spellings are accepted:
// README documents delete_retention (review R1), retention is the legacy name.
func (c *Controller) getServiceRetentionPolicy(labels map[string]string, serviceName string) types.RetentionPolicy {
	for _, key := range []string{
		"docktunnel." + serviceName + ".delete_retention",
		"docktunnel." + serviceName + ".retention",
		"docktunnel.delete_retention",
		"docktunnel.retention",
	} {
		if v, ok := labels[key]; ok {
			policy, err := label.ParseRetentionPolicy(v)
			if err != nil {
				slog.Warn("Ignoring invalid retention policy value",
					"label", key, "value", v, "error", err)
				continue
			}
			return policy
		}
	}

	// Default: Immediate
	return types.RetentionPolicy{Type: types.Immediate}
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
				for serviceName, rule := range parsedRules {
					policy := c.getServiceRetentionPolicy(labels, serviceName)
					ruleJSON, _ := marshalIngressRule(rule)
					hostname := ""
					if rule.Hostname.Value != "" {
						hostname = rule.Hostname.Value
					}
					c.stateManager.AddActiveTunnel(&types.TunnelEntry{
						ContainerID:     event.ContainerID,
						ServiceName:     serviceName,
						RetentionPolicy: policy,
						Status:          types.StatusActive,
						CreatedAt:       now,
						LastSyncAt:      now,
						// B12: fill Hostname (and RuleJSON) so downstream GC /
						// buildDesiredRules can act on the correct route.
						Config: types.TunnelConfiguration{
							Hostname: hostname,
							RuleJSON: ruleJSON,
						},
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

	// Transition each service independently (shared with the disconnect
	// difference-set in buildDesiredRules, B8)
	allActions := c.transitionEntriesStopped(event.ContainerID, entries)

	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			// Keys are (hostname, path) since B10 — remove every path of the
			// hostname.
			deleteIngressByHostnameLocked(c, action.Hostname)
		}
	}
	c.mu.Unlock()
	c.updateContainerHealth(event.ContainerID, false)

	// NOTE: sync failures are compensated by the syncWorker onError callback,
	// which enqueues a single ActionSync (B3). The old per-action enqueue here
	// was unreachable because syncToCloudflare never returns an error.
	return c.syncToCloudflare(ctx)
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
		deleteIngressByHostnameLocked(c, hostname)
	}
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
