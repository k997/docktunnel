package cloudflareManager

import (
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	// 跳过需要实际API调用的测试
	// 这些测试需要有效的Cloudflare账户和API令牌
	t.Skip("Skipping test that requires valid Cloudflare credentials")

	// 测试创建Cloudflare管理器
	opts := ManagerOptions{
		AccountID:     "test-account-id",
		APIToken:      "test-api-token",
		TunnelID:      "",
		TunnelName:    "",
		RateLimit:     10,
		MaxRetries:    3,
		RetryDelay:    1 * time.Second,
		MaxRetryDelay: 30 * time.Second,
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	if manager == nil {
		t.Error("Manager should not be nil")
	}

	// 注意：由于需要有效的API令牌，我们不能进行实际的API调用测试
	// 这些测试主要验证结构是否正确创建
}

func TestNewManagerWithInvalidConfig(t *testing.T) {
	// 测试使用无效配置创建Cloudflare管理器（仅测试参数验证）
	testCases := []struct {
		name string
		opts ManagerOptions
	}{
		{
			name: "Empty account ID",
			opts: ManagerOptions{
				AccountID:  "",
				APIToken:   "test-api-token",
				TunnelID:   "",
				TunnelName: "",
			},
		},
		{
			name: "Empty API token",
			opts: ManagerOptions{
				AccountID:  "test-account-id",
				APIToken:   "",
				TunnelID:   "",
				TunnelName: "",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			manager, err := NewManager(tc.opts)
			if err == nil {
				t.Error("Expected error but got none")
			}

			if manager != nil {
				t.Error("Manager should be nil when config is invalid")
			}
		})
	}
}

func TestManagerOptions(t *testing.T) {
	// 测试ManagerOptions结构体
	opts := ManagerOptions{
		AccountID:     "test-account",
		APIToken:      "test-token",
		TunnelID:      "test-tunnel-id",
		TunnelName:    "test-tunnel-name",
		RateLimit:     10,
		MaxRetries:    3,
		RetryDelay:    1 * time.Second,
		MaxRetryDelay: 30 * time.Second,
	}

	if opts.AccountID != "test-account" {
		t.Errorf("Expected AccountID to be 'test-account', got %s", opts.AccountID)
	}

	if opts.APIToken != "test-token" {
		t.Errorf("Expected APIToken to be 'test-token', got %s", opts.APIToken)
	}

	if opts.TunnelID != "test-tunnel-id" {
		t.Errorf("Expected TunnelID to be 'test-tunnel-id', got %s", opts.TunnelID)
	}

	if opts.TunnelName != "test-tunnel-name" {
		t.Errorf("Expected TunnelName to be 'test-tunnel-name', got %s", opts.TunnelName)
	}

	if opts.RateLimit != 10 {
		t.Errorf("Expected RateLimit to be 10, got %d", opts.RateLimit)
	}

	if opts.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries to be 3, got %d", opts.MaxRetries)
	}

	if opts.RetryDelay != 1*time.Second {
		t.Errorf("Expected RetryDelay to be 1s, got %v", opts.RetryDelay)
	}

	if opts.MaxRetryDelay != 30*time.Second {
		t.Errorf("Expected MaxRetryDelay to be 30s, got %v", opts.MaxRetryDelay)
	}
}

func TestGetOrCreateTunnel(t *testing.T) {
	// TODO: 实现GetOrCreateTunnel方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("GetOrCreateTunnel test placeholder")
}

func TestUpdateConfiguration(t *testing.T) {
	// TODO: 实现UpdateConfiguration方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("UpdateConfiguration test placeholder")
}

func TestGetTunnelToken(t *testing.T) {
	// TODO: 实现GetTunnelToken方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("GetTunnelToken test placeholder")
}

func TestUpsertDNSRecords(t *testing.T) {
	// TODO: 实现UpsertDNSRecords方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("UpsertDNSRecords test placeholder")
}

func TestDeleteDNSRecords(t *testing.T) {
	// TODO: 实现DeleteDNSRecords方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("DeleteDNSRecords test placeholder")
}
