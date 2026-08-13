package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
	"golang.org/x/time/rate"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/option"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/cloudflare/cloudflare-go/v5/zones"
)

const DEFAULT_TUNNEL_NAME = "DockTunnel"

// maxBatchSize 是单次 Cloudflare DNS 批量接口允许提交的最大操作数。
// Cloudflare 批量接口存在单次上限，200 为保守值：超大部署按此分块循环提交，
// 避免单批过大导致整个 zone 的操作整体失败。
const maxBatchSize = 200

// zoneCacheTTL 是 zone ID 缓存的有效期。超过该时长视为失效重新查询，
// 避免 zone 被删除/转移后长期命中过期缓存。
const zoneCacheTTL = 10 * time.Minute

// generateTunnelSecret 生成一个用于Cloudflare Tunnel的随机secret。
// Cloudflare 创建隧道 API 要求 tunnel_secret 为「至少 32 字节、base64 编码」的字符串
// （见 SDK zero_trust/tunnelcloudflared.go 中 TunnelSecret 字段的注释），
// 因此这里对 32 字节随机密钥做标准 base64 编码，而不是 hex 编码。
func generateTunnelSecret() (string, error) {
	// 创建一个32字节的随机密钥
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	if err != nil {
		return "", err
	}

	// 将密钥编码为 base64 字符串（32 字节 → 44 字符，含填充）
	return base64.StdEncoding.EncodeToString(secret), nil
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
	// hostname到zoneID的缓存映射（key 为派生 domain，如 "example.com"）
	zoneCache       map[string]string
	zoneCacheExpiry map[string]time.Time
	cacheMu         sync.RWMutex

	// 速率限制器
	rateLimiter *rate.Limiter

	// 重试配置
	maxRetries    int
	retryDelay    time.Duration
	maxRetryDelay time.Duration
}

// newHTTPClient 构造带超时的专用 http.Client。
// SDK 默认使用 http.DefaultClient（无任何超时），网络悬挂会导致启动/同步永久阻塞，
// 因此这里显式配置连接建立、TLS 握手、响应头与整体请求超时作为兜底。
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
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

	// 创建Cloudflare API客户端（注入带超时的专用 HTTP client，见 newHTTPClient）
	client := cloudflare.NewClient(
		option.WithAPIToken(opts.APIToken),
		option.WithHTTPClient(newHTTPClient()),
	)

	manager := &Manager{
		client:          client,
		account:         opts.AccountID,
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      opts.MaxRetries,
		retryDelay:      opts.RetryDelay,
		maxRetryDelay:   opts.MaxRetryDelay,
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

		// 指数退避 + full jitter：在 [0, backoff) 之间取随机值，避免重试风暴
		backoff := min(m.retryDelay*time.Duration(1<<uint(attempt)), m.maxRetryDelay)
		delay := time.Duration(mathrand.Float64() * float64(backoff))

		// 429 时若服务端返回 Retry-After，则至少等待该时长（否则可能立即重试再次被打回）
		if retryAfter := retryAfterDelay(err); retryAfter > delay {
			delay = retryAfter
		}

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

// retryAfterDelay 从 429 的 typed API error 中提取 Retry-After 指示的等待时长。
//
// cloudflare-go v5 的 *cloudflare.Error（internal/apierror.Error 的别名）没有
// 独立的 Headers 字段，响应头挂在 Response *http.Response 上，因此这里通过
// apiErr.Response.Header 读取。支持两种格式：
//   - delta-seconds：Retry-After: 120
//   - HTTP-date：Retry-After: Wed, 21 Oct 2026 07:28:00 GMT（http.ParseTime）
//
// 未设置、非 429 或无法解析时返回 0。
func retryAfterDelay(err error) time.Duration {
	var apiErr *cloudflare.Error
	if !errors.As(err, &apiErr) {
		return 0
	}
	if apiErr.StatusCode != 429 || apiErr.Response == nil {
		return 0
	}
	header := strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
	if header == "" {
		return 0
	}

	// 1) delta-seconds 格式
	if secs, convErr := strconv.Atoi(header); convErr == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}

	// 2) HTTP-date 格式
	if when, parseErr := http.ParseTime(header); parseErr == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
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
// waitRateLimit，避免单个逻辑操作突破 RPS 上限。创建成功后立即按名再查一次，
// 防止 5xx 重试导致服务端创建多份重复 tunnel。
func (m *Manager) getOrCreateTunnel(ctx context.Context, tunnelID, tunnelName string) (*zero_trust.TunnelCloudflaredGetResponse, error) {
	// 1. 如果提供了tunnelID，尝试获取现有的tunnel
	if tunnelID != "" {
		found, err := m.getTunnelByID(ctx, tunnelID)
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

	// 按名查找（分页遍历账户下全部隧道，避免只扫第一页漏掉匹配项）
	matches, err := m.listTunnelsByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}
	if len(matches) > 0 {
		slog.Info("Found existing tunnel by name", "tunnelName", name, "tunnelID", matches[0].ID)
		return m.getTunnelByID(ctx, matches[0].ID)
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

	// 4. 创建成功后按名复查，确认唯一（用与第 2 步相同的分页查找函数）。
	// 5xx 重试可能导致服务端创建多份同名 tunnel：复查时发现多个同名项则记录 Error
	// （仍返回第一个匹配的完整对象，不阻塞启动）。
	matches, err = m.listTunnelsByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to re-check tunnel by name after creation: %w", err)
	}
	if len(matches) == 0 {
		// 刚创建的 tunnel 因最终一致性暂未出现在按名列表中，回退为按 ID 获取完整对象
		slog.Warn("Created tunnel not visible in by-name re-check, falling back to Get by ID",
			"tunnelID", created.ID, "tunnelName", created.Name)
		return m.getTunnelByID(ctx, created.ID)
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, t := range matches {
			ids = append(ids, t.ID)
		}
		slog.Error("Duplicate tunnels detected after creation",
			"tunnelName", name, "count", len(matches), "tunnelIDs", ids)
	}

	return m.getTunnelByID(ctx, matches[0].ID)
}

// listTunnelsByName 分页遍历账户下全部隧道，返回所有名称匹配的隧道。
// 使用 SDK 的 ListAutoPaging 遍历所有页，避免只扫第一页 Result 漏掉匹配项。
func (m *Manager) listTunnelsByName(ctx context.Context, name string) ([]zero_trust.TunnelListResponse, error) {
	var matches []zero_trust.TunnelListResponse
	err := m.callWithRetry(ctx, func() error {
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		// 每次尝试都重置，重试不会累积重复条目
		matches = nil
		pager := m.client.ZeroTrust.Tunnels.ListAutoPaging(ctx, zero_trust.TunnelListParams{
			AccountID: cloudflare.F(m.account),
		})
		for pager.Next() {
			if pager.Current().Name == name {
				matches = append(matches, pager.Current())
			}
		}
		if err := pager.Err(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// getTunnelByID 按 ID 获取隧道的完整对象（带重试与速率限制）。
func (m *Manager) getTunnelByID(ctx context.Context, tunnelID string) (*zero_trust.TunnelCloudflaredGetResponse, error) {
	var found *zero_trust.TunnelCloudflaredGetResponse
	if err := m.callWithRetry(ctx, func() error {
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		g, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
			AccountID: cloudflare.F(m.account),
		})
		if err != nil {
			return err
		}
		found = g
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to get tunnel %s: %w", tunnelID, err)
	}
	return found, nil
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
//
// 缓存项带 TTL（zoneCacheTTL=10 分钟）：命中缓存时检查过期时间，过期视为
// 未命中重新查询，避免 zone 被删除/转移后长期命中过期缓存。
func (m *Manager) getZoneIDForHostname(ctx context.Context, hostname string) (string, error) {
	candidates := zoneLookupCandidates(hostname)
	if len(candidates) == 0 {
		return "", fmt.Errorf("invalid hostname: %q", hostname)
	}

	// 检查缓存：用所有候选 domain 依次查（带 TTL 校验）
	m.cacheMu.RLock()
	for _, dom := range candidates {
		if zoneID, exists := m.zoneCache[dom]; exists {
			if exp, ok := m.zoneCacheExpiry[dom]; ok && time.Now().Before(exp) {
				m.cacheMu.RUnlock()
				return zoneID, nil
			}
			// 缓存项已过期（或未记录过期时间）：视为 miss，继续尝试下一候选
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
			// 缓存结果（用 domain 作为 key，记录过期时间）
			m.cacheMu.Lock()
			if m.zoneCache == nil {
				m.zoneCache = make(map[string]string)
			}
			if m.zoneCacheExpiry == nil {
				m.zoneCacheExpiry = make(map[string]time.Time)
			}
			m.zoneCache[dom] = zoneID
			m.zoneCacheExpiry[dom] = time.Now().Add(zoneCacheTTL)
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
		// 获取所有zone（分页遍历全部页，Zones 默认每页 20 条，避免大账户漏 zone）
		if err := m.waitRateLimit(ctx); err != nil {
			return err
		}
		allTunnelRecords = []dns.RecordResponse{}
		zonePager := m.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{})

		var failedZones []string
		var succeededZones int
		var firstErr error
		for zonePager.Next() {
			z := zonePager.Current()

			// 列出zone中的所有CNAME记录（分页遍历全部页，默认每页 100 条，
			// 避免大 zone 漏记录导致孤儿 DNS 与重复建记录）。
			// 注意：ListAutoPaging 的翻页请求由 SDK 在 Next() 内部发起，
			// 无法逐页插入 waitRateLimit，此处仅保证每个 zone 的首次请求受限。
			if err := m.waitRateLimit(ctx); err != nil {
				return err
			}
			recPager := m.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
				ZoneID: cloudflare.F(z.ID),
				Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
			})
			for recPager.Next() {
				record := recPager.Current()
				// 过滤出指向当前隧道的记录（大小写与 trailing dot 归一化）
				if normalizeCNAMEContent(record.Content) == expectedContent {
					allTunnelRecords = append(allTunnelRecords, record)
				}
			}
			if err := recPager.Err(); err != nil {
				// 记录该 zone 与错误，继续处理其他 zone
				slog.Warn("Failed to list DNS records for zone",
					"zone", z.Name, "zoneID", z.ID, "error", err)
				failedZones = append(failedZones, z.Name)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			succeededZones++
		}
		if err := zonePager.Err(); err != nil {
			return err
		}
		// P3-4: 全部 zone 都失败时返回聚合错误，让外层 callWithRetry 按退避
		// 重试整个操作——否则闭包恒返回 nil，整个操作"成功"返回空结果，调用方
		// 无法感知。部分失败保持现状（汇总 Error 日志后返回 nil，靠下轮
		// reconcile 收敛）。
		if len(failedZones) > 0 && succeededZones == 0 {
			return fmt.Errorf("failed to list DNS records for all %d zone(s) (first failure in zone %q: %w)",
				len(failedZones), failedZones[0], firstErr)
		}
		// 若存在失败的 zone，输出一条汇总 Error 日志（含失败 zone 数量）
		if len(failedZones) > 0 {
			slog.Error("Failed to list DNS records for some zones",
				"failedZones", len(failedZones), "succeededZones", succeededZones)
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

// collectDeleteIDs flattens a set of DNS records into batch-delete payloads,
// skipping any record whose normalized content does not equal expectedContent.
// 这是纵深防御：只有 content 归一化后等于本隧道 content 的记录才允许删除，
// 防止未来调用方变化导致误删其它服务的同名记录。
func collectDeleteIDs(records []dns.RecordResponse, expectedContent string) []dns.RecordBatchParamsDelete {
	out := make([]dns.RecordBatchParamsDelete, 0, len(records))
	for _, record := range records {
		if normalizeCNAMEContent(record.Content) != normalizeCNAMEContent(expectedContent) {
			slog.Warn("Skipping DNS record deletion: content does not match tunnel target",
				"recordID", record.ID, "name", record.Name,
				"content", record.Content, "expectedContent", expectedContent)
			continue
		}
		out = append(out, dns.RecordBatchParamsDelete{ID: cloudflare.F(record.ID)})
	}
	return out
}

// upsertOp 表示一条 DNS 更新操作：新建（post）或更新（patch）。
// 用统一的操作列表按 maxBatchSize 分块，保证每批提交的总条数不超过上限。
type upsertOp struct {
	post  dns.RecordBatchParamsPostUnion
	patch dns.BatchPatchUnionParam
}

// UpsertDNSRecords 批量创建或更新DNS记录。
//
// 为每个 hostname 先查现有 CNAME：已存在的放进 Patches（按 record ID 更新），
// 不存在的放进 Posts（新建）。如果只发 Posts，重复 hostname 会让 Cloudflare
// 整个 batch 失败（错误码 81057），导致该 zone 一条记录都没更新。
//
// 数据安全：命中已有同名记录时，仅当其 content 归一化后等于本隧道 content
// 才走 PATCH；若同名记录指向其它目标，则跳过该 hostname（既不覆盖也不新建），
// 拒绝改写用户既有记录。
//
// 批量提交按每批最多 maxBatchSize 条分块循环提交，避免超大部署整体失败。
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
		var ops []upsertOp

		for _, hostname := range zoneHosts {
			// 查现有 CNAME：DNS 大小写不敏感，按 lowercase key 索引
			nameParam := dns.RecordListParamsName{Exact: cloudflare.F(hostname)}
			var existing []dns.RecordResponse
			err := m.callWithRetry(ctx, func() error {
				records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
					ZoneID: cloudflare.F(zoneID),
					Name:   cloudflare.F(nameParam),
					Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
				})
				if err != nil {
					return err
				}
				existing = records.Result
				return nil
			})
			if err != nil {
				return fmt.Errorf("failed to list existing DNS records for hostname %s: %w", hostname, err)
			}

			// 命中已有记录：仅当 content 归一化后等于本隧道 content 才走 PATCH；
			// 否则跳过该 hostname（不 PATCH 也不 POST），拒绝覆盖用户既有记录
			rec := matchExistingCNAME(existing, hostname)
			if rec == nil {
				// 不存在同名记录：新建
				ops = append(ops, upsertOp{post: dns.CNAMERecordParam{
					Name:    cloudflare.F(hostname),
					Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
					Content: cloudflare.F(content),
					Proxied: cloudflare.F(true),
					TTL:     cloudflare.F(dns.TTL1),
				}})
				continue
			}
			if normalizeCNAMEContent(rec.Content) != normalizeCNAMEContent(content) {
				slog.Warn("Skipping hostname: existing CNAME points to another target",
					"hostname", hostname, "recordID", rec.ID,
					"existingContent", rec.Content, "tunnelContent", content)
				continue
			}
			ops = append(ops, upsertOp{patch: dns.BatchPatchCNAMERecordParam{
				ID: cloudflare.F(rec.ID),
				CNAMERecordParam: dns.CNAMERecordParam{
					Name:    cloudflare.F(hostname),
					Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
					Content: cloudflare.F(content),
					Proxied: cloudflare.F(true),
					TTL:     cloudflare.F(dns.TTL1),
				},
			}})
		}

		// 分块提交：每批最多 maxBatchSize 条（posts+patches 合计），
		// 避免超大 zone 的单批请求超过 Cloudflare 批量接口上限而整体失败
		for start := 0; start < len(ops); start += maxBatchSize {
			end := min(start+maxBatchSize, len(ops))
			chunk := ops[start:end]

			var posts []dns.RecordBatchParamsPostUnion
			var patches []dns.BatchPatchUnionParam
			for _, op := range chunk {
				if op.post != nil {
					posts = append(posts, op.post)
				} else {
					patches = append(patches, op.patch)
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
	}

	return nil
}

// DeleteDNSRecords 批量删除DNS记录
//
// 删除前对每条记录做 content 校验（纵深防御）：仅删除 content 归一化后等于
// 本隧道 content 的记录，防止未来调用方变化导致误删其它服务的记录。
// 批量提交按每批最多 maxBatchSize 条分块循环提交。
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

	// 确保tunnel存在（content 校验需要本隧道 ID）
	if m.tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Deleting DNS records",
		"action", "dns_delete",
		"hostnames", hostnames)

	content := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

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

				// 仅收集 content 归一化后等于本隧道 content 的记录，
				// 不符的跳过并 Warn（防御未来调用方变化）
				found = collectDeleteIDs(records.Result, content)
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

		// 执行批量删除（分块提交，每批最多 maxBatchSize 条）
		for start := 0; start < len(deletes); start += maxBatchSize {
			end := min(start+maxBatchSize, len(deletes))
			slog.Info("Performing batch deletion", "zoneID", zoneID, "deleteCount", end-start)

			batchParams := dns.RecordBatchParams{
				ZoneID:  cloudflare.F(zoneID),
				Deletes: cloudflare.F(deletes[start:end]),
			}

			// 使用重试机制执行批量删除
			err := m.callWithRetry(ctx, func() error {
				_, err := m.client.DNS.Records.Batch(ctx, batchParams)
				return err
			})

			if err != nil {
				return fmt.Errorf("failed to batch delete DNS records for zone %s: %w", zoneID, err)
			}

			slog.Info("Batch deletion completed successfully", "zoneID", zoneID, "deletedCount", end-start)
		}
		if len(deletes) == 0 {
			slog.Debug("No records to delete for zone", "zoneID", zoneID)
		}
	}

	return nil
}
