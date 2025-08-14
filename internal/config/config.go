package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 定义了应用的完整配置项
type Config struct {
	Log struct {
		Level  string `yaml:"level"`  // 日志级别, 支持: "debug", "info", "warn", "error"
		Format string `yaml:"format"` // 日志格式, 支持: "text", "json"
	} `yaml:"log"`
	Cloudflare struct {
		AccountID string `yaml:"accountId"`
		// 使用 API Token 进行认证，这是 Cloudflare 推荐的最佳实践，无需 API Key 和 Email
		APIToken  string `yaml:"apiToken"`
		TunnelID  string `yaml:"tunnelId"`
		TunnelName string `yaml:"tunnelName"`
	} `yaml:"cloudflare"`
	// Cleanup选项控制程序退出时是否清理生成的资源
	Cleanup struct {
		// OnExit控制程序退出时是否清理DNS记录和删除tunnel
		OnExit bool `yaml:"onExit"`
	} `yaml:"cleanup"`
}

// Load 从指定路径加载配置文件
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	// 设置默认值
	if config.Cleanup.OnExit != false {
		// 默认情况下启用清理功能
		config.Cleanup.OnExit = true
	}

	return &config, nil
}

// Validate 验证配置是否有效
func (c *Config) Validate() error {
	if c.Cloudflare.AccountID == "" {
		return fmt.Errorf("cloudflare accountId is required")
	}
	
	if c.Cloudflare.APIToken == "" {
		return fmt.Errorf("cloudflare apiToken is required")
	}
	
	// TunnelID 可以为空，如果为空将在运行时自动创建
	
	// 验证日志级别
	switch c.Log.Level {
	case "debug", "info", "warn", "error", "":
		// 有效值或空值（将使用默认值）
	default:
		return fmt.Errorf("invalid log level: %s", c.Log.Level)
	}
	
	// 验证日志格式
	switch c.Log.Format {
	case "text", "json", "":
		// 有效值或空值（将使用默认值）
	default:
		return fmt.Errorf("invalid log format: %s", c.Log.Format)
	}
	
	return nil
}

// GetLogLevel 返回日志级别，如果未设置则返回默认值
func (c *Config) GetLogLevel() string {
	if c.Log.Level == "" {
		return "info"
	}
	return c.Log.Level
}

// GetLogFormat 返回日志格式，如果未设置则返回默认值
func (c *Config) GetLogFormat() string {
	if c.Log.Format == "" {
		return "text"
	}
	return c.Log.Format
}

// ShouldCleanupOnExit 返回是否在退出时清理资源，默认为true
func (c *Config) ShouldCleanupOnExit() bool {
	return c.Cleanup.OnExit
}