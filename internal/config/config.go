package config

import (
	"strings"
	"github.com/spf13/viper"
)

// Config 定义了应用的完整配置项
type Config struct {
	Log struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
	} `mapstructure:"log"`
	Cloudflare struct {
		AccountID  string `mapstructure:"accountId"`
		APIToken   string `mapstructure:"apiToken"`
		TunnelID   string `mapstructure:"tunnelId"`
		TunnelName string `mapstructure:"tunnelName"`
		CatchAll   string `mapstructure:"catchAll"`
	} `mapstructure:"cloudflare"`
	Cleanup struct {
		OnExit bool `mapstructure:"onExit"`
	} `mapstructure:"cleanup"`
}

// New 初始化并返回一个配置实例
func New() (*Config, error) {
	v := viper.New()

	// 1. 设置默认值
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("cloudflare.tunnelName", "DockTunnel") // 默认通道名称
	v.SetDefault("cloudflare.catchAll", "http_status:404") // 默认catch-all规则
	v.SetDefault("cleanup.onExit", true)

	// 2. 设置配置文件
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".") // 在当前目录查找
	v.AddConfigPath("/etc/docktunnel") // 在/etc/docktunnel目录查找

	// 3. 绑定环境变量
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 4. 读取配置
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// 配置文件被找到但解析错误
			return nil, err
		}
		// 配置文件未找到，可以忽略，因为我们有默认值
	}

	// 5. 解析到结构体
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}