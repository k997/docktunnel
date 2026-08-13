package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/metrics"

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

	// Compute the full desired state: running containers plus persisted
	// retention entries (B1). buildDesiredRules also reconciles state entries
	// for containers that started/stopped during downtime.
	desired, err := s.c.buildDesiredRules(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to build desired rules: %w", err)
	}

	slog.Info("Found containers with docktunnel labels", "count", len(desired.containerIDs))

	// 更新内部状态 — 全量替换为新的期望状态
	// (Validation moved into buildDesiredRules: per-container/per-service
	// isolation, review R6.)
	s.c.mu.Lock()
	s.c.ingressRules = desired.rules
	s.c.containerRules = desired.containerHostnames
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
	// Keys are compound (hostname\x00path) since B10 — extract the hostname
	// part so one DNS record covers all paths of a hostname.
	currentHostnames := make(map[string]bool)
	for key := range s.c.ingressRules {
		currentHostnames[hostnameFromKey(key)] = true
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

	// Deterministic ordering: sort by (hostname, path, service) so the rule
	// list is stable across restarts and independent of map iteration order
	// (B10). Stored rules always carry a hostname.
	sort.Slice(rules, func(i, j int) bool {
		hi, hj := rules[i].Hostname.Value, rules[j].Hostname.Value
		if hi != hj {
			return hi < hj
		}
		pi, pj := rules[i].Path.Value, rules[j].Path.Value
		if pi != pj {
			return pi < pj
		}
		return rules[i].Service.Value < rules[j].Service.Value
	})

	// 总是添加catch-all规则作为最后一个规则 (B4: 使用配置的 catch-all service)
	rules = append(rules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F(s.c.getCatchAllService()),
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
