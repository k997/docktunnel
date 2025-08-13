package config

import (
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
	} `yaml:"cloudflare"`
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

	return &config, nil
}