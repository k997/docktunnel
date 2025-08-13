package cloudflareManager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudflare/cloudflare-go"
)

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
	client    *cloudflare.API
	accountID string
	tunnelID  string
	// hostname到zoneID的缓存映射
	zoneCache map[string]string
	cacheMu   sync.RWMutex
}

// NewManager 创建一个新的Cloudflare Manager实例
func NewManager(accountID, apiToken, tunnelID string) (*Manager, error) {
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

	return &Manager{
		client:    client,
		accountID: accountID,
		tunnelID:  tunnelID,
		zoneCache: make(map[string]string),
	}, nil
}

// 测试连接是否正常
func (m *Manager) ValidateConnection(ctx context.Context) error {
	// 尝试列出区域来验证凭证是否有效
	_, err := m.client.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to cloudflare: invalid credentials or network issue: %w", err)
	}
	return nil
}

// GetOrCreateTunnel 检查Tunnel是否存在，如果不存在则创建一个新的
func (m *Manager) GetOrCreateTunnel(ctx context.Context, tunnelName string) (string, error) {
	// 创建账户资源容器
	accountResource := cloudflare.AccountIdentifier(m.accountID)

	// 如果tunnelID已经设置，验证它是否存在
	if m.tunnelID != "" {
		_, err := m.client.GetTunnel(ctx, accountResource, m.tunnelID)
		if err != nil {
			return "", fmt.Errorf("failed to get tunnel %s: %w", m.tunnelID, err)
		}
		return m.tunnelID, nil
	}

	// 如果没有设置tunnelID，尝试查找同名的tunnel
	tunnels, _, err := m.client.ListTunnels(ctx, accountResource, cloudflare.TunnelListParams{
		Name: tunnelName,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list tunnels: %w", err)
	}

	// 如果找到同名tunnel，使用它
	if len(tunnels) > 0 {
		m.tunnelID = tunnels[0].ID
		return m.tunnelID, nil
	}

	// 生成一个随机secret
	secret, err := generateTunnelSecret()
	if err != nil {
		return "", fmt.Errorf("failed to generate tunnel secret: %w", err)
	}

	// 如果没有找到同名tunnel，创建一个新的
	tunnel, err := m.client.CreateTunnel(ctx, accountResource, cloudflare.TunnelCreateParams{
		Name:      tunnelName,
		Secret:    secret,           // 使用生成的secret
		ConfigSrc: "cloudflare", // 使用云端配置
	})
	if err != nil {
		return "", fmt.Errorf("failed to create tunnel: %w", err)
	}

	m.tunnelID = tunnel.ID
	return m.tunnelID, nil
}

// UpdateConfiguration 更新Tunnel的配置
func (m *Manager) UpdateConfiguration(ctx context.Context, ingressRules []cloudflare.UnvalidatedIngressRule) error {
	if m.tunnelID == "" {
		return fmt.Errorf("tunnel ID is not set")
	}

	// 创建账户资源容器
	accountResource := cloudflare.AccountIdentifier(m.accountID)

	// 创建配置参数对象
	configParams := cloudflare.TunnelConfigurationParams{
		TunnelID: m.tunnelID,
		Config: cloudflare.TunnelConfiguration{
			Ingress: ingressRules,
		},
	}

	// 更新Tunnel配置
	_, err := m.client.UpdateTunnelConfiguration(ctx, accountResource, configParams)
	if err != nil {
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	return nil
}

// GetTunnelToken 获取Tunnel的令牌
func (m *Manager) GetTunnelToken(ctx context.Context) (string, error) {
	if m.tunnelID == "" {
		return "", fmt.Errorf("tunnel ID is not set")
	}

	// 创建账户资源容器
	accountResource := cloudflare.AccountIdentifier(m.accountID)

	token, err := m.client.GetTunnelToken(ctx, accountResource, m.tunnelID)
	if err != nil {
		return "", fmt.Errorf("failed to get tunnel token: %w", err)
	}

	return token, nil
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
