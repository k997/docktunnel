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

func TestValidateConfig(t *testing.T) {
	// 测试有效配置
	validConfig := &Config{}
	validConfig.Cloudflare.AccountID = "account-id"
	validConfig.Cloudflare.APIToken = "api-token"
	
	err := validConfig.Validate()
	if err != nil {
		t.Errorf("Expected valid config, but got error: %v", err)
	}
	
	// 测试缺少 AccountID 的配置
	invalidConfig1 := &Config{}
	invalidConfig1.Cloudflare.APIToken = "api-token"
	
	err = invalidConfig1.Validate()
	if err == nil {
		t.Error("Expected error for missing AccountID, but got none")
	}
	
	// 测试缺少 APIToken 的配置
	invalidConfig2 := &Config{}
	invalidConfig2.Cloudflare.AccountID = "account-id"
	
	err = invalidConfig2.Validate()
	if err == nil {
		t.Error("Expected error for missing APIToken, but got none")
	}
	
	// 测试无效日志级别
	invalidConfig3 := &Config{}
	invalidConfig3.Cloudflare.AccountID = "account-id"
	invalidConfig3.Cloudflare.APIToken = "api-token"
	invalidConfig3.Log.Level = "invalid"
	
	err = invalidConfig3.Validate()
	if err == nil {
		t.Error("Expected error for invalid log level, but got none")
	}
	
	// 测试无效日志格式
	invalidConfig4 := &Config{}
	invalidConfig4.Cloudflare.AccountID = "account-id"
	invalidConfig4.Cloudflare.APIToken = "api-token"
	invalidConfig4.Log.Format = "invalid"
	
	err = invalidConfig4.Validate()
	if err == nil {
		t.Error("Expected error for invalid log format, but got none")
	}
}

func TestGetLogLevel(t *testing.T) {
	// 测试获取设置的日志级别
	config1 := &Config{}
	config1.Log.Level = "debug"
	if config1.GetLogLevel() != "debug" {
		t.Errorf("Expected 'debug', got '%s'", config1.GetLogLevel())
	}
	
	// 测试获取默认日志级别
	config2 := &Config{}
	if config2.GetLogLevel() != "info" {
		t.Errorf("Expected default 'info', got '%s'", config2.GetLogLevel())
	}
}

func TestGetLogFormat(t *testing.T) {
	// 测试获取设置的日志格式
	config1 := &Config{}
	config1.Log.Format = "json"
	if config1.GetLogFormat() != "json" {
		t.Errorf("Expected 'json', got '%s'", config1.GetLogFormat())
	}
	
	// 测试获取默认日志格式
	config2 := &Config{}
	if config2.GetLogFormat() != "text" {
		t.Errorf("Expected default 'text', got '%s'", config2.GetLogFormat())
	}
}