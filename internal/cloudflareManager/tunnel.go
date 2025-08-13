package cloudflareManager

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go"
)

// Manager 封装了所有Cloudflare Tunnel相关的操作
type Manager struct {
	client    *cloudflare.API
	accountID string
	tunnelID  string
}

// NewManager 创建一个新的Cloudflare Manager实例
func NewManager(accountID, apiToken, tunnelID string) (*Manager, error) {
	// 使用API令牌创建Cloudflare API客户端
	client, err := cloudflare.NewWithAPIToken(apiToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloudflare client: %w", err)
	}

	return &Manager{
		client:    client,
		accountID: accountID,
		tunnelID:  tunnelID,
	}, nil
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

	// 如果没有找到同名tunnel，创建一个新的
	tunnel, err := m.client.CreateTunnel(ctx, accountResource, cloudflare.TunnelCreateParams{
		Name:      tunnelName,
		Secret:    "", // 让Cloudflare生成secret
		ConfigSrc: "cloud", // 使用云端配置
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