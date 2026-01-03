package config

import (
	"strings"
	"time"
	"github.com/spf13/viper"
	
	"docktunnel/internal/cloudflareManager"
	"docktunnel/internal/controller"
)

// Config 定义了应用的完整配置项
type Config struct {
	Log struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
	} `mapstructure:"log"`
	Cloudflare struct {
		AccountID       string        `mapstructure:"accountId"`
		APIToken        string        `mapstructure:"apiToken"`
		TunnelID        string        `mapstructure:"tunnelId"`
		TunnelName      string        `mapstructure:"tunnelName"`
		CatchAll        string        `mapstructure:"catchAll"`
		// API调用相关配置
		RateLimit       int           `mapstructure:"rateLimit"`
		MaxRetries      int           `mapstructure:"maxRetries"`
		RetryDelay      time.Duration `mapstructure:"retryDelay"`
		MaxRetryDelay   time.Duration `mapstructure:"maxRetryDelay"`
	} `mapstructure:"cloudflare"`
	Controller struct {
		// 容器抖动检测配置
		FlappingWindow    time.Duration `mapstructure:"flappingWindow"`
		FlappingThreshold int           `mapstructure:"flappingThreshold"`
		CoolingPeriod     time.Duration `mapstructure:"coolingPeriod"`
		MaxCoolingPeriod  time.Duration `mapstructure:"maxCoolingPeriod"`
		DebounceDuration  time.Duration `mapstructure:"debounceDuration"`
	} `mapstructure:"controller"`
	Cleanup struct {
		OnExit    bool   `mapstructure:"onExit"`
		StateFile string `mapstructure:"stateFile"`
	} `mapstructure:"cleanup"`
	Defaults struct {
		Scheme string `mapstructure:"scheme"`
		Port   int    `mapstructure:"port"`
		Path   string `mapstructure:"path"`
	} `mapstructure:"defaults"`
}

// GetCloudflareOptions 从配置中获取Cloudflare选项
func (c *Config) GetCloudflareOptions() cloudflareManager.ManagerOptions {
	return cloudflareManager.ManagerOptions{
		AccountID:     c.Cloudflare.AccountID,
		APIToken:      c.Cloudflare.APIToken,
		TunnelID:      c.Cloudflare.TunnelID,
		TunnelName:    c.Cloudflare.TunnelName,
		RateLimit:     c.Cloudflare.RateLimit,
		MaxRetries:    c.Cloudflare.MaxRetries,
		RetryDelay:    c.Cloudflare.RetryDelay,
		MaxRetryDelay: c.Cloudflare.MaxRetryDelay,
	}
}

// GetControllerOptions 从配置中获取Controller选项
func (c *Config) GetControllerOptions() controller.ControllerOptions {
	return controller.ControllerOptions{
		CatchAllService:   c.Cloudflare.CatchAll,
		FlappingWindow:    c.Controller.FlappingWindow,
		FlappingThreshold: c.Controller.FlappingThreshold,
		CoolingPeriod:     c.Controller.CoolingPeriod,
		MaxCoolingPeriod:  c.Controller.MaxCoolingPeriod,
		DebounceDuration:  c.Controller.DebounceDuration,
	}
}

// New 初始化并返回一个配置实例
func New() (*Config, error) {
	v := viper.New()

	// 1. 设置默认值
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("cloudflare.tunnelName", "DockTunnel") // 默认通道名称
	v.SetDefault("cloudflare.catchAll", "http_status:404") // 默认catch-all规则
	v.SetDefault("cloudflare.rateLimit", 10) // 默认每秒10个请求的速率限制
	v.SetDefault("cloudflare.maxRetries", 3) // 默认最大重试次数
	v.SetDefault("cloudflare.retryDelay", 1*time.Second) // 默认初始重试延迟1秒
	v.SetDefault("cloudflare.maxRetryDelay", 30*time.Second) // 默认最大重试延迟30秒
	v.SetDefault("controller.flappingWindow", 60*time.Second) // 默认抖动检测窗口60秒
	v.SetDefault("controller.flappingThreshold", 5) // 默认抖动阈值5次重启
	v.SetDefault("controller.coolingPeriod", 300*time.Second) // 默认冷却期300秒(5分钟)
	v.SetDefault("controller.maxCoolingPeriod", 1800*time.Second) // 默认最大冷却期1800秒(30分钟)
	v.SetDefault("controller.debounceDuration", 2*time.Second) // 默认防抖延迟2秒
	v.SetDefault("cleanup.onExit", true)
	// Global defaults for label auto-detection fallback
	v.SetDefault("defaults.scheme", "http") // 默认scheme (http, https, tcp)
	v.SetDefault("defaults.port", 80)       // 默认端口
	v.SetDefault("defaults.path", "")       // 默认路径

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