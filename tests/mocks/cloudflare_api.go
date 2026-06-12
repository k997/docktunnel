package mocks

import (
	"context"

	"docktunnel/pkg/types"
)

// MockCloudflareClient is a mock implementation of the Cloudflare API client
type MockCloudflareClient struct {
	Tunnels    map[string]*types.TunnelEntry
	Configs    map[string]string // tunnelID -> config
	CallCount  int
	ShouldFail bool
	FailAfter  int
}

// NewMockCloudflareClient creates a new mock Cloudflare client
func NewMockCloudflareClient() *MockCloudflareClient {
	return &MockCloudflareClient{
		Tunnels:    make(map[string]*types.TunnelEntry),
		Configs:    make(map[string]string),
		ShouldFail: false,
	}
}

// GetTunnelConfig retrieves the current tunnel configuration
func (m *MockCloudflareClient) GetTunnelConfig(ctx context.Context, accountID, tunnelID string) (map[string]string, error) {
	m.CallCount++

	if m.ShouldFail && m.CallCount > m.FailAfter {
		return nil, &MockError{Message: "mock API failure"}
	}

	if config, exists := m.Configs[tunnelID]; exists {
		// Return mock config
		return map[string]string{"config": config}, nil
	}

	// Return empty config
	return map[string]string{}, nil
}

// UpdateTunnelConfig updates the tunnel configuration
func (m *MockCloudflareClient) UpdateTunnelConfig(ctx context.Context, accountID, tunnelID string, config map[string]string) error {
	m.CallCount++

	if m.ShouldFail && m.CallCount > m.FailAfter {
		return &MockError{Message: "mock API failure"}
	}

	m.Configs[tunnelID] = config["config"]
	return nil
}

// FindTunnelByName finds a tunnel by name
func (m *MockCloudflareClient) FindTunnelByName(ctx context.Context, accountID, name string) (string, error) {
	if m.ShouldFail {
		return "", &MockError{Message: "mock API failure"}
	}

	// Return a mock tunnel ID
	return "mock-tunnel-id", nil
}

// CreateTunnel creates a new tunnel
func (m *MockCloudflareClient) CreateTunnel(ctx context.Context, accountID, name string, secret []byte) (string, error) {
	if m.ShouldFail {
		return "", &MockError{Message: "mock API failure"}
	}

	tunnelID := "mock-tunnel-" + name
	m.Tunnels[tunnelID] = &types.TunnelEntry{}
	return tunnelID, nil
}

// MockError represents a mock API error
type MockError struct {
	Message string
}

func (e *MockError) Error() string {
	return e.Message
}
