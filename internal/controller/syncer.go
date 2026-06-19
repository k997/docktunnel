package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/label"
	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// syncer drives Cloudflare writes through the syncWorker and owns the
// actual-state cache used by /debug/state.
type syncer struct {
	c   *Controller
	log *slog.Logger

	actualStateMu        sync.RWMutex
	lastKnownActualRules []diagnostics.RuleView
}

func newSyncer(c *Controller, log *slog.Logger) *syncer {
	return &syncer{c: c, log: log}
}

// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (s *syncer) Sync(ctx context.Context) error {
	// 从cloudflareManager获取tunnel信息
	tunnel := s.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Starting synchronization", "action", "sync", "tunnelID", tunnel.ID)

	// 扫描运行中的容器
	eventsList, err := s.c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to scan running containers: %w", err)
	}

	slog.Info("Found containers with docktunnel labels", "count", len(eventsList))

	// Reconcile with persisted state (T074)
	// Detect containers that started during downtime and restore them if needed
	reconciledCount := 0
	for _, event := range eventsList {
		if !s.c.isDocktunnelEnabled(event) {
			continue
		}

		containerID := event.ContainerID

		// Check if container is in pending deletions (was stopped, now restarted)
		if pendingEntry, exists := s.c.stateManager.GetPendingDeletion(containerID); exists {
			slog.Info("Container restarted during downtime, restoring from pending deletion",
				"containerID", containerID,
				"service_name", pendingEntry.ServiceName,
			)

			// Restore to active state
			if err := s.c.stateManager.RestoreActiveTunnel(containerID); err != nil {
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
		if !s.c.isDocktunnelEnabled(event) {
			continue
		}

		// 解析标签获取主机名
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse container labels during sync",
				"action", "parse_labels",
				"result", "failure",
				"containerID", event.ContainerID,
				"error", err)
			continue
		}

		// 合并规则，确保服务名唯一
		for serviceName, rule := range parsedRules {
			if _, exists := allParsedRules[serviceName]; exists {
				slog.Warn("Duplicate service name found during sync, skipping.", "service_name", serviceName, "container_id", event.ContainerID)
				continue
			}
			allParsedRules[serviceName] = rule

			// Populate per-service state entries if not already present
			if _, exists := s.c.stateManager.GetActiveTunnel(event.ContainerID, serviceName); !exists {
				labels := event.ContainerInfo.Config.Labels
				policy := s.c.getServiceRetentionPolicy(labels, serviceName)
				hostname := ""
				if rule.Hostname.Value != "" {
					hostname = rule.Hostname.Value
				}
				s.c.stateManager.AddActiveTunnel(&types.TunnelEntry{
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
	if err := s.c.ruleValidator.Validate(allParsedRules, nil); err != nil {
		slog.Error("Invalid ingress rules during sync",
			"action", "validate_rules",
			"result", "failure",
			"error", err)
		return fmt.Errorf("invalid ingress rules during sync: %w", err)
	}

	// 更新内部状态
	s.c.mu.Lock()
	// 重新创建ingress规则map
	s.c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)

	// 添加所有新规则
	for _, rule := range allParsedRules {
		// 添加所有有主机名的规则
		if rule.Hostname.Value != "" {
			s.c.ingressRules[rule.Hostname.Value] = *rule
		}
	}

	// 更新容器与主机名的关联关系
	s.c.containerRules = containerHostnames
	s.c.mu.Unlock()

	// 同步到Cloudflare
	if err := s.syncToCloudflare(ctx); err != nil {
		return err
	}

	// Refresh the actual-state cache for /debug/state (Phase 6).
	// Non-fatal: a failure here just means diagnostics show stale data.
	s.refreshActualState(ctx)

	return nil
}

// syncToCloudflare signals the sync worker that a sync is desired.
// Returns immediately; actual Cloudflare write happens in the worker
// goroutine after the debounce window. ctx is unused but kept for
// call-site compatibility.
func (s *syncer) syncToCloudflare(_ context.Context) error {
	s.c.syncWorker.TriggerSync()
	return nil
}

// performSync 执行实际的Cloudflare同步操作
func (s *syncer) performSync(ctx context.Context) error {
	syncStart := time.Now()

	// 构建规则列表
	ingressRules := s.GetIngressRules()

	slog.Info("Performing sync with rules", "ruleCount", len(ingressRules))

	// 从cloudflareManager获取tunnel信息
	tunnel := s.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		slog.Error("Tunnel is not available",
			"action", "sync",
			"result", "failure")
		metrics.ObserveSyncDuration(time.Since(syncStart).Seconds())
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Using tunnel", "tunnelID", tunnel.ID)

	// 更新配置（保持原始的ingress规则，不需要修改Service字段）
	if err := s.c.cloudflareManager.UpdateConfiguration(ctx, ingressRules); err != nil {
		slog.Error("Failed to update tunnel configuration",
			"action", "update_config",
			"result", "failure",
			"error", err)
		metrics.ObserveSyncDuration(time.Since(syncStart).Seconds())
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "ruleCount", len(ingressRules)-1) // -1 for catch-all rule

	// 同步DNS记录
	if err := s.syncDNSRecords(ctx); err != nil {
		slog.Error("Failed to sync DNS records",
			"action", "sync_dns",
			"result", "failure",
			"error", err)
		metrics.ObserveSyncDuration(time.Since(syncStart).Seconds())
		return fmt.Errorf("failed to sync DNS records: %w", err)
	}

	metrics.ObserveSyncDuration(time.Since(syncStart).Seconds())
	slog.Info("Sync operation completed successfully")
	return nil
}

// syncDNSRecords 同步DNS记录到Cloudflare
// 这个方法会确保Cloudflare中的DNS记录与当前ingress规则保持一致
// 使用批量API操作来减少对Cloudflare的访问压力
func (s *syncer) syncDNSRecords(ctx context.Context) error {
	// 获取当前隧道信息
	tunnel := s.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Debug("Starting DNS records sync", "tunnelID", tunnel.ID)

	s.c.mu.RLock()
	// Collect hostnames from ingressRules (source of truth for active routes).
	// This correctly preserves DNS for Timed/Forever retention where
	// containerRules is cleared but ingressRules are kept.
	currentHostnames := make(map[string]bool)
	for hostname := range s.c.ingressRules {
		currentHostnames[hostname] = true
	}
	s.c.mu.RUnlock()

	slog.Info("Syncing DNS records", "expectedHostnamesCount", len(currentHostnames), "expectedHostnames", currentHostnames)

	// 先获取Cloudflare上当前的所有DNS记录，以减少API访问次数
	allTunnelRecords, err := s.c.cloudflareManager.ListDNSRecords(ctx)
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
		if err := s.c.cloudflareManager.DeleteDNSRecords(ctx, deleteHostnames); err != nil {
			slog.Error("Failed to batch delete DNS records",
				"action", "dns_delete",
				"result", "failure",
				"error", err)
			return err
		}
		slog.Info("Batch deleted DNS records", "count", len(deleteHostnames))
	}

	// 执行批量创建/更新操作
	if len(upsertHostnames) > 0 {
		if err := s.c.cloudflareManager.UpsertDNSRecords(ctx, upsertHostnames); err != nil {
			slog.Error("Failed to batch upsert DNS records",
				"action", "dns_upsert",
				"result", "failure",
				"error", err)
			return err
		}
		slog.Info("Batch upserted DNS records", "count", len(upsertHostnames))
	}

	slog.Info("Finished syncing DNS records")

	return nil
}

// GetIngressRules 获取当前的Ingress规则
func (s *syncer) GetIngressRules() []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()

	// 创建规则切片
	rules := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(s.c.ingressRules)+1)

	// 添加所有规则
	for _, rule := range s.c.ingressRules {
		rules = append(rules, rule)
	}

	// 总是添加catch-all规则作为最后一个规则
	rules = append(rules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http_status:404"),
	})

	return rules
}

// refreshActualState fetches the live tunnel config from Cloudflare and
// caches it for /debug/state. Safe to call from Sync/Reconcile.
func (s *syncer) refreshActualState(ctx context.Context) {
	ingress, err := s.c.cloudflareManager.GetConfiguration(ctx)
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
	s.setLastKnownActualRules(views)
}

// setLastKnownActualRules replaces the cached actual-state snapshot.
// Called by Sync/Reconcile after a successful UpdateConfiguration push.
func (s *syncer) setLastKnownActualRules(rules []diagnostics.RuleView) {
	s.actualStateMu.Lock()
	s.lastKnownActualRules = rules
	s.actualStateMu.Unlock()
}

// FlushSync blocks until the worker has processed one sync cycle triggered by this call.
func (s *syncer) FlushSync(ctx context.Context) error {
	return s.c.syncWorker.FlushSync(ctx)
}
