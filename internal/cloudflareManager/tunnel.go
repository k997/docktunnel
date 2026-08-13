package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
	"golang.org/x/time/rate"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/option"
	"github.com/cloudflare/cloudflare-go/v5/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/cloudflare/cloudflare-go/v5/zones"
)

const DEFAULT_TUNNEL_NAME = "DockTunnel"

// generateTunnelSecret 生成一个用于Cloudflare Tunnel的随机secret
func generateTunnelSecret() (string, error) {
	// 创建一个32字节的随机密钥
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	if err != nil {
		return "", err
	}

	// 将密钥编码为十六进制字符串
	return hex.EncodeToString(secret), nil
}

// ManagerOptions 用于配置Manager的选项
type ManagerOptions struct {
	AccountID  string
	APIToken   string
	TunnelID   string
	TunnelName string
	// 速率限制配置 (每秒请求数，0表示无限制)
	RateLimit int
	// 重试配置
	MaxRetries    int
	RetryDelay    time.Duration
	MaxRetryDelay time.Duration
}

// Manager 封装了所有Cloudflare Tunnel相关的操作
type Manager struct {
	client  *cloudflare.Client
	account string // account ID as string instead of ResourceContainer
	tunnel  *zero_trust.TunnelCloudflaredGetResponse
	// hostname到zoneID的缓存映射
	zoneCache map[string]string
	cacheMu   sync.RWMutex

	// 速率限制器
	rateLimiter *rate.Limiter

	// 重试配置
	maxRetries    int
	retryDelay    time.Duration
	maxRetryDelay time.Duration
}

// NewManager 创建一个新的Cloudflare Manager实例
func NewManager(opts ManagerOptions) (*Manager, error) {
	// 验证必要参数
	if opts.AccountID == "" {
		return nil, fmt.Errorf("accountID cannot be empty")
	}

	if opts.APIToken == "" {
		return nil, fmt.Errorf("apiToken cannot be empty")
	}

	// 创建Cloudflare API客户端
	client := cloudflare.NewClient(
		option.WithAPIToken(opts.APIToken),
	)

	manager := &Manager{
		client:        client,
		account:       opts.AccountID,
		zoneCache:     make(map[string]string),
		maxRetries:    opts.MaxRetries,
		retryDelay:    opts.RetryDelay,
		maxRetryDelay: opts.MaxRetryDelay,
	}

	// 设置默认重试配置
	if manager.maxRetries == 0 {
		manager.maxRetries = 3
	}
	if manager.retryDelay == 0 {
		manager.retryDelay = 1 * time.Second
	}
	if manager.maxRetryDelay == 0 {
		manager.maxRetryDelay = 30 * time.Second
	}

	// 设置速率限制器
	if opts.RateLimit > 0 {
		// 使用令牌桶算法，桶大小为速率限制值，补充速率为每秒令牌数
		manager.rateLimiter = rate.NewLimiter(rate.Limit(opts.RateLimit), opts.RateLimit)
	} else {
		// 默认不限制速率
		manager.rateLimiter = rate.NewLimiter(rate.Inf, 0)
	}

	// 获取或创建Tunnel
	tunnel, err := manager.getOrCreateTunnel(context.Background(), opts.TunnelID, opts.TunnelName)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create tunnel: %w", err)
	}
	manager.tunnel = tunnel

	return manager, nil
}

// callWithRetry 使用指数退避和重试机制执行API调用。
//
// maxRetries 表示初始尝试之外的额外重试次数：maxRetries=3 共发起最多 4 次尝试。
//
// 注意：rate limit 只在这里 wait 一次。如果 operation 内部发起多个 HTTP 调用，
// 调用方必须自己用 waitRateLimit(ctx) 在每个 HTTP 调用前 wait，否则实际 RPS
// 会突破配置上限、触发 429。
func (m *Manager) callWithRetry(ctx context.Context, operation func() error) error {
	var lastErr error

	// 等待获取令牌（针对 operation 整体的入口等待）
	if err := m.waitRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	for attempt := 0; attempt <= m.maxRetries; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		// 如果是上下文取消或超时错误，直接返回
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 不可重试的错误（4xx 非 429、权限问题等）立即返回
		if !isRetriableError(err) {
			return &types.PermanentError{Err: err}
		}

		// 最后一次不再 sleep
		if attempt == m.maxRetries {
			break
		}

		// 指数退避 + 抖动
		backoff := min(m.retryDelay*time.Duration(1<<uint(attempt)), m.maxRetryDelay)
		jitter := time.Duration(float64(backoff) * 0.1 * (0.5 - mathrand.Float64()))
		delay := backoff + jitter

		slog.Warn("Cloudflare API call failed, retrying",
			"attempt", attempt+1,
			"maxRetries", m.maxRetries,
			"delay", delay,
			"error", err)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return &types.RetryableError{Err: lastErr}
}

// waitRateLimit blocks until the rate limiter allows one event. Safe to call
// from inside an operation passed to callWithRetry — needed for operations
// that issue multiple HTTP calls (e.g. ListDNSRecords lists all zones, then
// one DNS list per zone).
func (m *Manager) waitRateLimit(ctx context.Context) error {
	if m.rateLimiter == nil {
		return nil
	}
	return m.rateLimiter.Wait(ctx)
}

// isRetriableError 判断错误是否值得重试。
//
// 优先使用 cloudflare-go 的 typed error（*cloudflare.Error）的 StatusCode，
// 字符串匹配仅作为非 API 错误（网络层 timeout / connection refused 等）的兜底。
// 旧实现只做字符串匹配，会被错误 body 中的 status 数字（如 request ID）误判。
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	// 1) Cloudflare API typed error：直接看 StatusCode
	var apiErr *cloudflare.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 429:
			return true
		case apiErr.StatusCode >= 500 && apiErr.StatusCode < 600:
			return true
		default:
			return false
		}
	}

	// 2) 非 API 错误：网络层 timeout / connection / 5xx 字面（部分代理/网关
	// 不会把错误体包装成 cloudflare.Error，只在 message 里带 status）。
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "eof") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "service unavailable") ||
		strings.Contains(errStr, "bad gateway") ||
		strings.Contains(errStr, "gateway timeout") {
		return true
	}

	return false
}

// classifyError wraps an error as RetryableError or PermanentError based on
// whether it would normally be retried. Exported for callers that need the
// same classification without running the retry loop.
func classifyError(err error) error {
	if isRetriableError(err) {
		return &types.RetryableError{Err: err}
	}
	return &types.PermanentError{Err: err}
}

// getOrCreateTunnel 获取或创建Cloudflare Tunnel。
//
// 每个外部 API 调用都走 callWithRetry，并在多 HTTP 调用路径中显式调用
// waitRateLimit，避免单个逻辑操作突破 RPS 上限。创建后立即按名再查一次，
// 防止 5xx 重试导致服务端创建多份重复 tunnel。
func (m *Manager) getOrCreateTunnel(ctx context.Context, tunnelID, tunnelName string) (*zero_trust.TunnelCloudflaredGetResponse, error) {
	// 1. 如果提供了tunnelID，尝试获取现有的tunnel
	if tunnelID != "" {
		var found *zero_trust.TunnelCloudflaredGetResponse
		err := m.callWithRetry(ctx, func() error {
			if err := m.waitRateLimit(ctx); err != nil {
				return err
			}
			t, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
				AccountID: cloudflare.F(m.account),
			})
			if err != nil {
				return err
			}
			found = t
			return nil
		})
		if err == nil {
			slog.Info("Found existing tunnel by ID", "tunnelID", tunnelID)
			return found, nil
		}
		slog.Warn("Failed to get tunnel by ID, will try to find by name", "tunnelID", tunnelID, "error", err)
	}

	// 2. 如果没有提供tunnelID或者通过ID找不到，尝试通过名称查找
	name := tunnelName
	if name == "" {
		name = DEFAULT_TUNNEL_NAME
	}

	// 列出所有tunnel
	var tunnels *pagination.V4PagePaginationArray[zero_trust.TunnelListResponse]
	if err := m.callWithRetry(ctx, func() error {
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		t, err := m.client.ZeroTrust.Tunnels.List(ctx, zero_trust.TunnelListParams{
			AccountID: cloudflare.F(m.account),
		})
		if err != nil {
			return err
		}
		tunnels = t
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	// 查找匹配名称的tunnel
	for _, t := range tunnels.Result {
		if t.Name == name {
			slog.Info("Found existing tunnel by name", "tunnelName", name, "tunnelID", t.ID)
			var found *zero_trust.TunnelCloudflaredGetResponse
			if err := m.callWithRetry(ctx, func() error {
				if err := m.waitRateLimit(ctx); err != nil {
					return err
				}
				g, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, t.ID, zero_trust.TunnelCloudflaredGetParams{
					AccountID: cloudflare.F(m.account),
				})
				if err != nil {
					return err
				}
				found = g
				return nil
			}); err != nil {
				return nil, fmt.Errorf("failed to get tunnel %s: %w", t.ID, err)
			}
			return found, nil
		}
	}

	// 3. 如果找不到现有的tunnel，创建一个新的
	slog.Info("Creating new tunnel", "tunnelName", name)

	// 生成tunnel secret
	secret, err := generateTunnelSecret()
	if err != nil {
		return nil, fmt.Errorf("failed to generate tunnel secret: %w", err)
	}

	// 创建tunnel
	var created *zero_trust.TunnelCloudflaredNewResponse
	if err := m.callWithRetry(ctx, func() error {
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		c, err := m.client.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
			AccountID:    cloudflare.F(m.account),
			Name:         cloudflare.F(name),
			TunnelSecret: cloudflare.F(secret),
		})
		if err != nil {
			return err
		}
		created = c
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to create tunnel: %w", err)
	}

	slog.Info("Created new tunnel", "tunnelID", created.ID, "tunnelName", created.Name)

	// 创建成功后再 Get 一次拿完整对象
	var tunnelGet *zero_trust.TunnelCloudflaredGetResponse
	if err := m.callWithRetry(ctx, func() error {
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		g, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, created.ID, zero_trust.TunnelCloudflaredGetParams{
			AccountID: cloudflare.F(m.account),
		})
		if err != nil {
			return err
		}
		tunnelGet = g
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get created tunnel: %w", err)
	}

	return tunnelGet, nil
}

// GetTunnel 获取当前的Tunnel信息
func (m *Manager) GetTunnel() *zero_trust.TunnelCloudflaredGetResponse {
	return m.tunnel
}

// UpdateConfiguration 更新Tunnel的配置
func (m *Manager) UpdateConfiguration(ctx context.Context, ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	if m.tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	// 构造配置参数
	configParams := zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cloudflare.F(m.account),
		Config: cloudflare.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
			Ingress: cloudflare.F(ingressRules),
		}),
	}

	// 使用重试机制执行更新操作
	err := m.callWithRetry(ctx, func() error {
		_, err := m.client.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, m.tunnel.ID, configParams)
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	slog.Info("Updated tunnel configuration", "tunnelID", m.tunnel.ID)
	return nil
}

// GetConfiguration fetches the current tunnel configuration from Cloudflare.
// Returns the ingress rules currently configured on the tunnel.
func (m *Manager) GetConfiguration(ctx context.Context) ([]zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress, error) {
	if m.tunnel == nil {
		return nil, fmt.Errorf("tunnel is not available")
	}

	params := zero_trust.TunnelCloudflaredConfigurationGetParams{
		AccountID: cloudflare.F(m.account),
	}

	var result []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress
	err := m.callWithRetry(ctx, func() error {
		resp, err := m.client.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, m.tunnel.ID, params)
		if err != nil {
			return err
		}
		result = resp.Config.Ingress
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get tunnel configuration: %w", err)
	}

	return result, nil
}

// getZoneIDForHostname 获取主机名对应的zone ID
//
// Cache key is the derived base domain (e.g. "example.com"), not the full
// hostname. Otherwise every distinct subdomain misses the cache and triggers
// a fresh Zones.List call, defeating the cache and amplifying API usage.
//
// To handle domains under multi-label suffixes (e.g. "app.example.co.uk"),
// we walk hostname labels from right to left, trying progressively longer
// suffixes against Zones.List. The first match wins; this naturally handles
// both "example.com" (2-label) and "example.co.uk" (3-label) zones.
func (m *Manager) getZoneIDForHostname(ctx context.Context, hostname string) (string, error) {
	candidates := zoneLookupCandidates(hostname)
	if len(candidates) == 0 {
		return "", fmt.Errorf("invalid hostname: %q", hostname)
	}

	// 检查缓存：用所有候选 domain 依次查
	m.cacheMu.RLock()
	for _, dom := range candidates {
		if zoneID, exists := m.zoneCache[dom]; exists {
			m.cacheMu.RUnlock()
			return zoneID, nil
		}
	}
	m.cacheMu.RUnlock()

	// 依次尝试每个候选 domain
	var lastErr error
	for _, dom := range candidates {
		var zoneID string
		var found bool
		err := m.callWithRetry(ctx, func() error {
			zonesList, err := m.client.Zones.List(ctx, zones.ZoneListParams{
				Name: cloudflare.F(dom),
			})
			if err != nil {
				return err
			}
			if len(zonesList.Result) == 0 {
				// no match — try next candidate
				return nil
			}
			zoneID = zonesList.Result[0].ID
			found = true
			return nil
		})
		if err != nil {
			lastErr = err
			continue
		}
		if found {
			// 缓存结果（用 domain 作为 key）
			m.cacheMu.Lock()
			m.zoneCache[dom] = zoneID
			m.cacheMu.Unlock()
			return zoneID, nil
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, lastErr)
	}
	return "", fmt.Errorf("no zone found for hostname %s (tried candidates %v)", hostname, candidates)
}

// zoneLookupCandidates returns domain suffixes to try against Zones.List,
// ordered from most-specific to least-specific. Skips single-label TLDs.
// e.g. "app.example.co.uk" → ["app.example.co.uk", "example.co.uk", "co.uk"]
// but stops before ["uk"] (single label).
func zoneLookupCandidates(hostname string) []string {
	h := strings.ToLower(strings.TrimSpace(hostname))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return nil
	}
	parts := strings.Split(h, ".")
	var out []string
	for i := 0; i < len(parts)-1; i++ { // skip single-label (i == len(parts)-1)
		out = append(out, strings.Join(parts[i:], "."))
	}
	return out
}

// normalizeCNAMEContent normalizes a CNAME content string for comparison.
// DNS is case-insensitive and the trailing dot is optional, so we lowercase
// and strip a single trailing dot. Without this, records returned by the
// Cloudflare API as "abc.cfargotunnel.com." would not match our expected
// "abc.cfargotunnel.com" and would be missed during cleanup scans.
func normalizeCNAMEContent(s string) string {
	return strings.TrimSuffix(strings.ToLower(s), ".")
}

// ListDNSRecords 列出所有与当前隧道相关的DNS记录
// 此方法会获取账户下所有zone，然后查找所有指向当前隧道的DNS记录
func (m *Manager) ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error) {
	// 确保tunnel存在
	if m.tunnel == nil {
		return nil, fmt.Errorf("tunnel is not available")
	}

	var allTunnelRecords []dns.RecordResponse
	expectedContent := normalizeCNAMEContent(fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID))

	// 使用重试机制执行操作
	err := m.callWithRetry(ctx, func() error {
		// 获取所有zone
		zonesList, err := m.client.Zones.List(ctx, zones.ZoneListParams{})
		if err != nil {
			return err
		}

		// 收集所有与当前隧道相关的DNS记录
		allTunnelRecords = []dns.RecordResponse{}

		// 遍历所有zone
		for _, z := range zonesList.Result {
			// 每个内部 HTTP 调用都要 wait rate limit，否则多 zone 遍历会突破 RPS 上限
			if err := m.waitRateLimit(ctx); err != nil {
				return err
			}
			// 列出zone中的所有CNAME记录
			records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
				ZoneID: cloudflare.F(z.ID),
				Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
			})
			if err != nil {
				// 如果某个zone访问失败，记录错误但继续处理其他zone
				slog.Warn("Failed to list DNS records for zone", "zone", z.Name, "error", err)
				continue
			}

			// 过滤出指向当前隧道的记录（大小写与 trailing dot 归一化）
			for _, record := range records.Result {
				if normalizeCNAMEContent(record.Content) == expectedContent {
					allTunnelRecords = append(allTunnelRecords, record)
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list DNS records: %w", err)
	}

	return allTunnelRecords, nil
}

// matchExistingCNAME returns the first record in records whose name matches
// hostname case-insensitively. DNS names are case-insensitive and Cloudflare's
// Exact name filter is case-sensitive, so a server-side Exact query still needs
// a client-side re-match. Trailing dots are ignored ("App.example.com." matches
// "app.example.com"). Returns nil when nothing matches.
func matchExistingCNAME(records []dns.RecordResponse, hostname string) *dns.RecordResponse {
	want := strings.ToLower(strings.TrimSuffix(hostname, "."))
	for i := range records {
		got := strings.ToLower(strings.TrimSuffix(records[i].Name, "."))
		if got == want {
			return &records[i]
		}
	}
	return nil
}

// collectDeleteIDs flattens a set of DNS records into batch-delete payloads.
func collectDeleteIDs(records []dns.RecordResponse) []dns.RecordBatchParamsDelete {
	out := make([]dns.RecordBatchParamsDelete, 0, len(records))
	for _, record := range records {
		out = append(out, dns.RecordBatchParamsDelete{ID: cloudflare.F(record.ID)})
	}
	return out
}

// UpsertDNSRecords 批量创建或更新DNS记录。
//
// 为每个 hostname 先查现有 CNAME：已存在的放进 Patches（按 record ID 更新），
// 不存在的放进 Posts（新建）。如果只发 Posts，重复 hostname 会让 Cloudflare
// 整个 batch 失败（错误码 81057），导致该 zone 一条记录都没更新。
func (m *Manager) UpsertDNSRecords(ctx context.Context, hostnames []string) (err error) {
	if len(hostnames) == 0 {
		return nil
	}

	// Record outcome for /metrics (Phase 6)
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		metrics.RecordDNSSync("upsert", result)
	}()

	// 确保tunnel存在
	if m.tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	// 按zone分组主机名
	zoneHostnames := make(map[string][]string)
	for _, hostname := range hostnames {
		zoneID, err := m.getZoneIDForHostname(ctx, hostname)
		if err != nil {
			return fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, err)
		}
		zoneHostnames[zoneID] = append(zoneHostnames[zoneID], hostname)
	}

	content := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

	// 为每个zone执行批量操作
	for zoneID, zoneHosts := range zoneHostnames {
		var posts []dns.RecordBatchParamsPostUnion
		var patches []dns.BatchPatchUnionParam

		for _, hostname := range zoneHosts {
			// 查现有 CNAME：DNS 大小写不敏感，按 lowercase key 索引
			existingID := ""
			nameParam := dns.RecordListParamsName{Exact: cloudflare.F(hostname)}
			err := m.callWithRetry(ctx, func() error {
				records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
					ZoneID: cloudflare.F(zoneID),
					Name:   cloudflare.F(nameParam),
					Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
				})
				if err != nil {
					return err
				}
				if rec := matchExistingCNAME(records.Result, hostname); rec != nil {
					existingID = rec.ID
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("failed to list existing DNS records for hostname %s: %w", hostname, err)
			}

			if existingID != "" {
				patches = append(patches, dns.BatchPatchCNAMERecordParam{
					ID: cloudflare.F(existingID),
					CNAMERecordParam: dns.CNAMERecordParam{
						Name:    cloudflare.F(hostname),
						Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
						Content: cloudflare.F(content),
						Proxied: cloudflare.F(true),
						TTL:     cloudflare.F(dns.TTL1),
					},
				})
			} else {
				posts = append(posts, dns.CNAMERecordParam{
					Name:    cloudflare.F(hostname),
					Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
					Content: cloudflare.F(content),
					Proxied: cloudflare.F(true),
					TTL:     cloudflare.F(dns.TTL1),
				})
			}
		}

		batchParams := dns.RecordBatchParams{
			ZoneID:  cloudflare.F(zoneID),
			Posts:   cloudflare.F(posts),
			Patches: cloudflare.F(patches),
		}

		if err := m.callWithRetry(ctx, func() error {
			_, err := m.client.DNS.Records.Batch(ctx, batchParams)
			return err
		}); err != nil {
			return fmt.Errorf("failed to batch upsert DNS records for zone %s: %w", zoneID, err)
		}
	}

	return nil
}

// DeleteDNSRecords 批量删除DNS记录
func (m *Manager) DeleteDNSRecords(ctx context.Context, hostnames []string) (err error) {
	if len(hostnames) == 0 {
		slog.Debug("No hostnames to delete")
		return nil
	}

	// Record outcome for /metrics (Phase 6)
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		metrics.RecordDNSSync("delete", result)
	}()

	slog.Info("Deleting DNS records",
		"action", "dns_delete",
		"hostnames", hostnames)

	// 按zone分组主机名
	zoneHostnames := make(map[string][]string)
	for _, hostname := range hostnames {
		zoneID, err := m.getZoneIDForHostname(ctx, hostname)
		if err != nil {
			return fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, err)
		}
		zoneHostnames[zoneID] = append(zoneHostnames[zoneID], hostname)
	}

	slog.Debug("Grouped hostnames by zone", "zoneGroups", zoneHostnames)

	// 为每个zone执行批量删除
	for zoneID, zoneHosts := range zoneHostnames {
		slog.Debug("Processing zone for deletion", "zoneID", zoneID, "hostnames", zoneHosts)

		// 先获取现有的记录ID
		// deletes 在 zone 范围累积。重试时每次都从本地 found 重新构建，
		// 只有调用成功才 swap 进 deletes，避免重试把同一记录 append 多次。
		var deletes []dns.RecordBatchParamsDelete

		for _, hostname := range zoneHosts {
			nameParam := dns.RecordListParamsName{Exact: cloudflare.F(hostname)}

			var found []dns.RecordBatchParamsDelete
			err := m.callWithRetry(ctx, func() error {
				// 每次尝试都重置 found，重试不会累积重复条目
				found = nil

				records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
					ZoneID: cloudflare.F(zoneID),
					Name:   cloudflare.F(nameParam),
					Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
				})
				if err != nil {
					return err
				}

				found = collectDeleteIDs(records.Result)
				return nil
			})

			if err != nil {
				return fmt.Errorf("failed to list DNS records for hostname %s: %w", hostname, err)
			}

			deletes = append(deletes, found...)
			if len(found) > 0 {
				slog.Debug("Collected records for deletion", "hostname", hostname, "count", len(found))
			}
		}

		// 执行批量删除
		if len(deletes) > 0 {
			slog.Info("Performing batch deletion", "zoneID", zoneID, "deleteCount", len(deletes))

			batchParams := dns.RecordBatchParams{
				ZoneID:  cloudflare.F(zoneID),
				Deletes: cloudflare.F(deletes),
			}

			// 使用重试机制执行批量删除
			err := m.callWithRetry(ctx, func() error {
				_, err := m.client.DNS.Records.Batch(ctx, batchParams)
				return err
			})

			if err != nil {
				return fmt.Errorf("failed to batch delete DNS records for zone %s: %w", zoneID, err)
			}

			slog.Info("Batch deletion completed successfully", "zoneID", zoneID, "deletedCount", len(deletes))
		} else {
			slog.Debug("No records to delete for zone", "zoneID", zoneID)
		}
	}

	return nil
}
