package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/pkg/types"
)

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
