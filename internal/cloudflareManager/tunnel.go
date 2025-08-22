package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"

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

// Manager 封装了所有Cloudflare Tunnel相关的操作
type Manager struct {
	client  *cloudflare.Client
	account string // account ID as string instead of ResourceContainer
	tunnel  *zero_trust.TunnelCloudflaredGetResponse
	// hostname到zoneID的缓存映射
	zoneCache map[string]string
	cacheMu   sync.RWMutex
}

// NewManager 创建一个新的Cloudflare Manager实例
func NewManager(accountID, apiToken, tunnelID, tunnelName string) (*Manager, error) {
	// 验证必要参数
	if accountID == "" {
		return nil, fmt.Errorf("accountID cannot be empty")
	}

	if apiToken == "" {
		return nil, fmt.Errorf("apiToken cannot be empty")
	}

	// 创建Cloudflare API客户端
	client := cloudflare.NewClient(
		option.WithAPIToken(apiToken),
	)

	manager := &Manager{
		client:    client,
		account:   accountID,
		zoneCache: make(map[string]string),
	}

	// 获取或创建Tunnel
	tunnel, err := manager.getOrCreateTunnel(context.Background(), tunnelID, tunnelName)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create tunnel: %w", err)
	}
	manager.tunnel = tunnel

	return manager, nil
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

	// 更新配置
	_, err := m.client.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, m.tunnel.ID, configParams)
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

	// 查询Cloudflare API获取zone信息
	zonesList, err := m.client.Zones.List(ctx, zones.ZoneListParams{
		Name: cloudflare.F(domain),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list zones: %w", err)
	}

	if len(zonesList.Result) == 0 {
		return "", fmt.Errorf("no zone found for domain: %s", domain)
	}

	// 使用第一个匹配的zone
	zoneID := zonesList.Result[0].ID

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

	// 获取所有zone
	zonesList, err := m.client.Zones.List(ctx, zones.ZoneListParams{})
	if err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}

	// 收集所有与当前隧道相关的DNS记录
	var allTunnelRecords []dns.RecordResponse
	expectedContent := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

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

		// 执行批量创建
		batchParams := dns.RecordBatchParams{
			ZoneID: cloudflare.F(zoneID),
			Posts:  cloudflare.F(posts),
		}

		_, err := m.client.DNS.Records.Batch(ctx, batchParams)

		if err != nil {
			return fmt.Errorf("failed to batch upsert DNS records for zone %s: %w", zoneID, err)
		}
	}

	return nil
}

// DeleteDNSRecords 批量删除DNS记录
func (m *Manager) DeleteDNSRecords(ctx context.Context, hostnames []string) error {
	if len(hostnames) == 0 {
		return nil
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

	// 为每个zone执行批量删除
	for zoneID, zoneHosts := range zoneHostnames {
		// 先获取现有的记录ID
		var deletes []dns.RecordBatchParamsDelete
		nameParam := dns.RecordListParamsName{}

		for _, hostname := range zoneHosts {
			nameParam.Exact = cloudflare.F(hostname)
			records, err := m.client.DNS.Records.List(ctx, dns.RecordListParams{
				ZoneID: cloudflare.F(zoneID),
				Name:   cloudflare.F(nameParam),
				Type:   cloudflare.F(dns.RecordListParamsTypeCNAME),
			})
			if err != nil {
				return fmt.Errorf("failed to list DNS records for hostname %s: %w", hostname, err)
			}

			// 收集记录ID用于删除
			for _, record := range records.Result {
				deletes = append(deletes, dns.RecordBatchParamsDelete{
					ID: cloudflare.F(record.ID),
				})
			}
		}

		// 执行批量删除
		if len(deletes) > 0 {
			batchParams := dns.RecordBatchParams{
				ZoneID:  cloudflare.F(zoneID),
				Deletes: cloudflare.F(deletes),
			}

			_, err := m.client.DNS.Records.Batch(ctx, batchParams)

			if err != nil {
				return fmt.Errorf("failed to batch delete DNS records for zone %s: %w", zoneID, err)
			}
		}
	}

	return nil
}
