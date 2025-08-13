package config

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// 创建临时配置文件用于测试
	tempConfig := `log:
  level: "debug"
  format: "json"
cloudflare:
  accountId: "test-account-id"
  apiToken: "test-api-token"
  tunnelId: "test-tunnel-id"
`

	// 写入临时文件
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(tempConfig)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// 测试加载配置
	config, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// 验证配置值
	if config.Log.Level != "debug" {
		t.Errorf("Expected log level 'debug', got '%s'", config.Log.Level)
	}

	if config.Log.Format != "json" {
		t.Errorf("Expected log format 'json', got '%s'", config.Log.Format)
	}

	if config.Cloudflare.AccountID != "test-account-id" {
		t.Errorf("Expected account ID 'test-account-id', got '%s'", config.Cloudflare.AccountID)
	}

	if config.Cloudflare.APIToken != "test-api-token" {
		t.Errorf("Expected API token 'test-api-token', got '%s'", config.Cloudflare.APIToken)
	}

	if config.Cloudflare.TunnelID != "test-tunnel-id" {
		t.Errorf("Expected tunnel ID 'test-tunnel-id', got '%s'", config.Cloudflare.TunnelID)
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	// 测试加载不存在的配置文件
	_, err := Load("non-existent-config.yaml")
	if err == nil {
		t.Error("Expected error when loading non-existent config file, but got none")
	}
}