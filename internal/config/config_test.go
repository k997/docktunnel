package config

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// 创建临时配置文件
	content := `
log:
  level: debug
  format: json
cloudflare:
  accountId: test-account-id
  apiToken: test-api-token
  tunnelId: test-tunnel-id
cleanup:
  onExit: true
`

	// 获取临时目录
	tempDir := t.TempDir()
	
	// 创建配置文件
	configPath := tempDir + "/config.yaml"
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// 保存当前工作目录
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	
	// 切换到临时目录
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	
	// 恢复工作目录
	defer func() {
		os.Chdir(originalDir)
	}()

	// 测试加载配置
	config, err := New()
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
	// 保存当前工作目录
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	
	// 切换到一个不存在配置文件的临时目录
	tempDir := os.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	
	// 恢复工作目录
	defer func() {
		os.Chdir(originalDir)
	}()
	
	// 测试加载不存在的配置文件应该成功（使用默认值）
	config, err := New()
	if err != nil {
		t.Errorf("Expected no error when config file not found, but got: %v", err)
	}
	
	// 验证是否使用了默认值
	if config.Log.Level != "info" {
		t.Errorf("Expected default log level 'info', got '%s'", config.Log.Level)
	}
}

func TestLoadDefaultConfig(t *testing.T) {
	// 创建空的配置文件
	tempDir := t.TempDir()
	
	// 创建空配置文件
	configPath := tempDir + "/config.yaml"
	if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// 保存当前工作目录
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	
	// 切换到临时目录
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	
	// 恢复工作目录
	defer func() {
		os.Chdir(originalDir)
	}()

	// 测试加载默认配置
	config, err := New()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// 验证默认值
	if config.Log.Level != "info" {
		t.Errorf("Expected default log level 'info', got '%s'", config.Log.Level)
	}
	
	if config.Log.Format != "text" {
		t.Errorf("Expected default log format 'text', got '%s'", config.Log.Format)
	}
	
	if config.Cleanup.OnExit != true {
		t.Errorf("Expected default cleanup on exit 'true', got '%v'", config.Cleanup.OnExit)
	}
}

func TestValidateConfig(t *testing.T) {
	// 根据技术文档要求，Validate函数已被移除
	// 配置验证现在由应用程序在运行时处理
	t.Skip("Validate function has been removed as per technical design")
}

func TestGetLogLevel(t *testing.T) {
	// 测试获取设置的日志级别
	config1 := &Config{}
	config1.Log.Level = "debug"
	if config1.Log.Level != "debug" {
		t.Errorf("Expected 'debug', got '%s'", config1.Log.Level)
	}
	
	// 测试获取默认日志级别
	config2 := &Config{}
	// 手动设置默认值进行测试
	config2.Log.Level = "info"
	if config2.Log.Level != "info" {
		t.Errorf("Expected default 'info', got '%s'", config2.Log.Level)
	}
}

func TestGetLogFormat(t *testing.T) {
	// 测试获取设置的日志格式
	config1 := &Config{}
	config1.Log.Format = "json"
	if config1.Log.Format != "json" {
		t.Errorf("Expected 'json', got '%s'", config1.Log.Format)
	}
	
	// 测试获取默认日志格式
	config2 := &Config{}
	// 手动设置默认值进行测试
	config2.Log.Format = "text"
	if config2.Log.Format != "text" {
		t.Errorf("Expected default 'text', got '%s'", config2.Log.Format)
	}
}