package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudflare/cloudflare-go"
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
	client  *cloudflare.API
	account *cloudflare.ResourceContainer
	tunnel  *cloudflare.Tunnel
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

	// 使用API令牌创建Cloudflare API客户端
	client, err := cloudflare.NewWithAPIToken(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare client: %w", err)
	}

	// 创建manager实例
	manager := &Manager{
		client:    client,
		account:   cloudflare.AccountIdentifier(accountID),
		zoneCache: make(map[string]string),
	}

	// 获取或创建隧道
	ctx := context.Background()
	tunnel, err := getOrCreateTunnel(client, ctx, manager.account, tunnelID, tunnelName)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create tunnel: %w", err)
	}

	manager.tunnel = &tunnel
	return manager, nil
}

// getOrCreateTunnel 检查Tunnel是否存在，如果不存在则创建一个新的
func getOrCreateTunnel(client *cloudflare.API, ctx context.Context, account *cloudflare.ResourceContainer, tunnelID, tunnelName string) (cloudflare.Tunnel, error) {
	// 如果提供了tunnelID，则初始化tunnel信息
	if tunnelID != "" {
		tunnel, err := client.GetTunnel(ctx, account, tunnelID)
		if err != nil {
			return cloudflare.Tunnel{}, fmt.Errorf("failed to get tunnel %s: %w", tunnelID, err)
		}
		return tunnel, nil
	}

	// 如果没有提供tunnelID，则根据tunnelName查找或创建隧道
	if tunnelName == "" {
		// 如果tunnelName为空，则使用默认名称
		tunnelName = "DockTunnel"
	}

	// 尝试查找同名的tunnel
	tunnels, _, err := client.ListTunnels(ctx, account, cloudflare.TunnelListParams{
		Name: tunnelName,
	})
	if err != nil {
		return cloudflare.Tunnel{}, fmt.Errorf("failed to list tunnels: %w", err)
	}

	// 如果找到同名tunnel，使用它
	if len(tunnels) > 0 {
		return tunnels[0], nil
	}

	// 生成一个随机secret
	secret, err := generateTunnelSecret()
	if err != nil {
		return cloudflare.Tunnel{}, fmt.Errorf("failed to generate tunnel secret: %w", err)
	}

	// 如果没有找到同名tunnel，创建一个新的
	tunnel, err := client.CreateTunnel(ctx, account, cloudflare.TunnelCreateParams{
		Name:      tunnelName,
		Secret:    secret,           // 使用生成的secret
		ConfigSrc: "cloudflare", // 使用云端配置
	})
	if err != nil {
		return cloudflare.Tunnel{}, fmt.Errorf("failed to create tunnel: %w", err)
	}

	return tunnel, nil
}

// UpdateConfiguration 更新Tunnel的配置
func (m *Manager) UpdateConfiguration(ctx context.Context, ingressRules []cloudflare.UnvalidatedIngressRule) error {
	if m.tunnel == nil || m.tunnel.ID == "" {
		return fmt.Errorf("tunnel is not set")
	}

	// 创建配置参数对象
	configParams := cloudflare.TunnelConfigurationParams{
		TunnelID: m.tunnel.ID,
		Config: cloudflare.TunnelConfiguration{
			Ingress: ingressRules,
		},
	}

	// 更新Tunnel配置
	_, err := m.client.UpdateTunnelConfiguration(ctx, m.account, configParams)
	if err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	return nil
}

// GetTunnelToken 获取Tunnel的令牌
func (m *Manager) GetTunnelToken(ctx context.Context) (string, error) {
	if m.tunnel == nil || m.tunnel.ID == "" {
		return "", fmt.Errorf("tunnel is not set")
	}

	token, err := m.client.GetTunnelToken(ctx, m.account, m.tunnel.ID)
	if err != nil {
		return "", fmt.Errorf("failed to get tunnel token: %w", err)
	}

	return token, nil
}

// GetTunnel 返回当前隧道信息
func (m *Manager) GetTunnel() *cloudflare.Tunnel {
	return m.tunnel
}

// getZoneIDForHostname 根据主机名获取Zone ID，首先检查缓存，如果缓存中没有则查询Cloudflare API
func (m *Manager) getZoneIDForHostname(ctx context.Context, hostname string) (string, error) {
	// 检查缓存
	m.cacheMu.RLock()
	if zoneID, exists := m.zoneCache[hostname]; exists {
		m.cacheMu.RUnlock()
		return zoneID, nil
	}
	m.cacheMu.RUnlock()

	// 从主机名提取域名部分（例如：从 api.example.com 提取 example.com）
	domain := hostname
	parts := strings.Split(hostname, ".")
	if len(parts) > 2 {
		// 取最后两个部分作为域名
		domain = strings.Join(parts[len(parts)-2:], ".")
	}

	// 查询Cloudflare API获取zone信息
	zones, err := m.client.ListZones(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("failed to list zones: %w", err)
	}

	if len(zones) == 0 {
		return "", fmt.Errorf("no zone found for domain: %s", domain)
	}

	// 使用第一个匹配的zone
	zoneID := zones[0].ID

	// 缓存结果
	m.cacheMu.Lock()
	m.zoneCache[hostname] = zoneID
	m.cacheMu.Unlock()

	return zoneID, nil
}

// UpsertDNSRecord 创建或更新DNS记录
func (m *Manager) UpsertDNSRecord(ctx context.Context, hostname, tunnelID string) error {
	// 获取zone ID
	zoneID, err := m.getZoneIDForHostname(ctx, hostname)
	if err != nil {
		return fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, err)
	}

	// 创建区域资源容器
	zoneResource := cloudflare.ZoneIdentifier(zoneID)

	// 构造CNAME记录内容，指向Cloudflare Tunnel
	content := fmt.Sprintf("%s.cfargotunnel.com", tunnelID)

	// 查找现有的DNS记录
	records, _, err := m.client.ListDNSRecords(ctx, zoneResource, cloudflare.ListDNSRecordsParams{
		Name: hostname,
		Type: "CNAME",
	})
	if err != nil {
		return fmt.Errorf("failed to list DNS records: %w", err)
	}

	// 如果记录已存在，更新它
	if len(records) > 0 {
		record := records[0]
		_, err = m.client.UpdateDNSRecord(ctx, zoneResource, cloudflare.UpdateDNSRecordParams{
			ID:      record.ID,
			Name:    hostname,
			Type:    "CNAME",
			Content: content,
			Proxied: cloudflare.BoolPtr(true),
			TTL:     1, // 自动TTL
		})
		if err != nil {
			return fmt.Errorf("failed to update DNS record: %w", err)
		}
	} else {
		// 如果记录不存在，创建新记录
		_, err = m.client.CreateDNSRecord(ctx, zoneResource, cloudflare.CreateDNSRecordParams{
			Name:    hostname,
			Type:    "CNAME",
			Content: content,
			Proxied: cloudflare.BoolPtr(true),
			TTL:     1, // 自动TTL
		})
		if err != nil {
			return fmt.Errorf("failed to create DNS record: %w", err)
		}
	}

	return nil
}

// ListDNSRecords 列出所有与当前隧道相关的DNS记录
// 此方法会获取账户下所有zone，然后查找所有指向当前隧道的DNS记录
func (m *Manager) ListDNSRecords(ctx context.Context) ([]cloudflare.DNSRecord, error) {
	// 确保tunnel存在
	if m.tunnel == nil {
		return nil, fmt.Errorf("tunnel is not available")
	}

	// 获取所有zone
	zones, err := m.client.ListZones(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list zones: %w", err)
	}

	// 收集所有与当前隧道相关的DNS记录
	var allTunnelRecords []cloudflare.DNSRecord
	expectedContent := fmt.Sprintf("%s.cfargotunnel.com", m.tunnel.ID)

	// 遍历所有zone
	for _, zone := range zones {
		// 创建区域资源容器
		zoneResource := cloudflare.ZoneIdentifier(zone.ID)

		// 列出zone中的所有CNAME记录
		records, _, err := m.client.ListDNSRecords(ctx, zoneResource, cloudflare.ListDNSRecordsParams{
			Type: "CNAME",
		})
		if err != nil {
			// 如果某个zone访问失败，记录错误但继续处理其他zone
			slog.Warn("Failed to list DNS records for zone", "zone", zone.Name, "error", err)
			continue
		}

		// 过滤出指向当前隧道的记录
		for _, record := range records {
			if record.Content == expectedContent {
				allTunnelRecords = append(allTunnelRecords, record)
			}
		}
	}

	return allTunnelRecords, nil
}


// DeleteDNSRecord 删除指定主机名的DNS记录
func (m *Manager) DeleteDNSRecord(ctx context.Context, hostname string) error {
	// 获取zone ID
	zoneID, err := m.getZoneIDForHostname(ctx, hostname)
	if err != nil {
		return fmt.Errorf("failed to get zone ID for hostname %s: %w", hostname, err)
	}

	// 创建区域资源容器
	zoneResource := cloudflare.ZoneIdentifier(zoneID)

	// 查找现有的DNS记录
	records, _, err := m.client.ListDNSRecords(ctx, zoneResource, cloudflare.ListDNSRecordsParams{
		Name: hostname,
		Type: "CNAME",
	})
	if err != nil {
		return fmt.Errorf("failed to list DNS records: %w", err)
	}

	// 删除所有匹配的记录
	for _, record := range records {
		err = m.client.DeleteDNSRecord(ctx, zoneResource, record.ID)
		if err != nil {
			return fmt.Errorf("failed to delete DNS record %s: %w", record.ID, err)
		}
	}

	return nil
}
