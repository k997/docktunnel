package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	mathrand "math/rand"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/option"
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

// callWithRetry 使用指数退避和重试机制执行API调用
func (m *Manager) callWithRetry(ctx context.Context, operation func() error) error {
	var lastErr error

	// 等待获取令牌
	if err := m.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	for i := 0; i <= m.maxRetries; i++ {
		err := operation()
		if err == nil {
			// 成功执行
			return nil
		}

		lastErr = err

		// 如果是上下文取消或超时错误，直接返回
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 检查是否是可重试的错误
		if !m.isRetriableError(err) {
			return err
		}

		// 如果不是最后一次重试，等待一段时间后重试
		if i < m.maxRetries {
			// 计算退避时间（指数退避加抖动）
			backoff := m.retryDelay * time.Duration(1<<uint(i))
			if backoff > m.maxRetryDelay {
				backoff = m.maxRetryDelay
			}

			// 添加随机抖动（±10%）
			jitter := time.Duration(float64(backoff) * 0.1 * (0.5 - mathrand.Float64()))
			delay := backoff + jitter

			slog.Warn("Cloudflare API call failed, retrying",
				"attempt", i+1,
				"maxRetries", m.maxRetries,
				"delay", delay,
				"error", err)

			select {
			case <-time.After(delay):
				// 继续重试
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return fmt.Errorf("operation failed after %d retries: %w", m.maxRetries, lastErr)
}

// isRetriableError 检查错误是否应该重试
func (m *Manager) isRetriableError(err error) bool {
	// 检查是否包含特定的错误信息
	errStr := err.Error()

	// 速率限制错误
	if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
		return true
	}

	// 服务器错误（5xx）
	if strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") || strings.Contains(errStr, "504") {
		return true
	}

	// 网络超时或连接错误
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "connection") {
		return true
	}

	return false
}

// getOrCreateTunnel 获取或创建Cloudflare Tunnel
func (m *Manager) getOrCreateTunnel(ctx context.Context, tunnelID, tunnelName string) (*zero_trust.TunnelCloudflaredGetResponse, error) {
	// 1. 如果提供了tunnelID，尝试获取现有的tunnel
	if tunnelID != "" {
		tunnel, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
			AccountID: cloudflare.F(m.account),
		})
		if err == nil {
			slog.Info("Found existing tunnel by ID", "tunnelID", tunnelID)
			return tunnel, nil
		}
		slog.Warn("Failed to get tunnel by ID, will try to find by name", "tunnelID", tunnelID, "error", err)
	}

	// 2. 如果没有提供tunnelID或者通过ID找不到，尝试通过名称查找
	name := tunnelName
	if name == "" {
		name = DEFAULT_TUNNEL_NAME
	}

	// 列出所有tunnel
	tunnels, err := m.client.ZeroTrust.Tunnels.List(ctx, zero_trust.TunnelListParams{
		AccountID: cloudflare.F(m.account),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list tunnels: %w", err)
	}

	// 查找匹配名称的tunnel
	for _, t := range tunnels.Result {
		if t.Name == name {
			slog.Info("Found existing tunnel by name", "tunnelName", name, "tunnelID", t.ID)
			tunnel, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, t.ID, zero_trust.TunnelCloudflaredGetParams{
				AccountID: cloudflare.F(m.account),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to get tunnel %s: %w", t.ID, err)
			}
			return tunnel, nil
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
	tunnel, err := m.client.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cloudflare.F(m.account),
		Name:         cloudflare.F(name),
		TunnelSecret: cloudflare.F(secret),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create tunnel: %w", err)
	}

	slog.Info("Created new tunnel", "tunnelID", tunnel.ID, "tunnelName", tunnel.Name)

	tunnelGet, err := m.client.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnel.ID, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cloudflare.F(m.account),
	})
	if err != nil {
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

// getZoneIDForHostname 获取主机名对应的zone ID
func (m *Manager) getZoneIDForHostname(ctx context.Context, hostname string) (string, error) {
	// 检查缓存
	m.cacheMu.RLock()
	if zoneID, exists := m.zoneCache[hostname]; exists {
		m.cacheMu.RUnlock()
		return zoneID, nil
	}
	m.cacheMu.RUnlock()

	// 从主机名提取域名部分
	domain := hostname
	parts := strings.Split(hostname, ".")
	if len(parts) > 2 {
		// 取最后两个部分作为域名
		domain = strings.Join(parts[len(parts)-2:], ".")
	}

	var zoneID string
	// 使用重试机制执行zone查询操作
	err := m.callWithRetry(ctx, func() error {
		// 查询Cloudflare API获取zone信息
		zonesList, err := m.client.Zones.List(ctx, zones.ZoneListParams{
			Name: cloudflare.F(domain),
		})
		if err != nil {
			return err
		}

		if len(zonesList.Result) == 0 {
			return fmt.Errorf("no zone found for domain: %s", domain)
		}

		// 使用第一个匹配的zone
		zoneID = zonesList.Result[0].ID
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, err)
	}

	// 缓存结果
	m.cacheMu.Lock()
	m.zoneCache[hostname] = zoneID
	m.cacheMu.Unlock()

	return zoneID, nil
}

// ListDNSRecords 列出所有与当前隧道相关的DNS记录
// 此方法会获取账户下所有zone，然后查找所有指向当前隧道的DNS记录
func (m *Manager) ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error) {
	// 确保tunnel存在
	if m.tunnel == nil {
		return nil, fmt.Errorf("tunnel is not available")
	}

	var allTunnelRecords []dns.RecordResponse
	expectedContent := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

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

			// 过滤出指向当前隧道的记录
			for _, record := range records.Result {
				if record.Content == expectedContent {
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

// UpsertDNSRecords 批量创建或更新DNS记录
func (m *Manager) UpsertDNSRecords(ctx context.Context, hostnames []string) error {
	if len(hostnames) == 0 {
		return nil
	}

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

	// 为每个zone执行批量操作
	for zoneID, zoneHosts := range zoneHostnames {
		var posts []dns.RecordBatchParamsPostUnion
		content := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

		for _, hostname := range zoneHosts {
			posts = append(posts, dns.CNAMERecordParam{
				Name:    cloudflare.F(hostname),
				Type:    cloudflare.F(dns.CNAMERecordTypeCNAME),
				Content: cloudflare.F(content),
				Proxied: cloudflare.F(true),
				TTL:     cloudflare.F(dns.TTL1),
			})
		}

		// 使用重试机制执行批量创建
		batchParams := dns.RecordBatchParams{
			ZoneID: cloudflare.F(zoneID),
			Posts:  cloudflare.F(posts),
		}

		err := m.callWithRetry(ctx, func() error {
			_, err := m.client.DNS.Records.Batch(ctx, batchParams)
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to batch upsert DNS records for zone %s: %w", zoneID, err)
		}
	}

	return nil
}

// DeleteDNSRecords 批量删除DNS记录
func (m *Manager) DeleteDNSRecords(ctx context.Context, hostnames []string) error {
	if len(hostnames) == 0 {
		slog.Debug("No hostnames to delete")
		return nil
	}

	slog.Info("Deleting DNS records", "hostnames", hostnames)

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
		var deletes []dns.RecordBatchParamsDelete
		nameParam := dns.RecordListParamsName{}

		for _, hostname := range zoneHosts {
			nameParam.Exact = cloudflare.F(hostname)
			// 使用重试机制获取记录
			err := m.callWithRetry(ctx, func() error {
				records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
					ZoneID: cloudflare.F(zoneID),
					Name:   cloudflare.F(nameParam),
					Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
				})
				if err != nil {
					return err
				}

				slog.Debug("Found records to delete", "hostname", hostname, "recordCount", len(records.Result))

				// 收集记录ID用于删除
				for _, record := range records.Result {
					deletes = append(deletes, dns.RecordBatchParamsDelete{
						ID: cloudflare.F(record.ID),
					})
					slog.Debug("Adding record for deletion", "recordID", record.ID, "hostname", hostname)
				}
				return nil
			})

			if err != nil {
				return fmt.Errorf("failed to list DNS records for hostname %s: %w", hostname, err)
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
