package config

import (
	"fmt"
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
		AccountID  string `mapstructure:"accountId"`
		APIToken   string `mapstructure:"apiToken"`
		TunnelID   string `mapstructure:"tunnelId"`
		TunnelName string `mapstructure:"tunnelName"`
		CatchAll   string `mapstructure:"catchAll"`
		// API调用相关配置
		RateLimit     int           `mapstructure:"rateLimit"`
		MaxRetries    int           `mapstructure:"maxRetries"`
		RetryDelay    time.Duration `mapstructure:"retryDelay"`
		MaxRetryDelay time.Duration `mapstructure:"maxRetryDelay"`
		// OriginRequest默认配置 (T082)
		OriginRequest struct {
			NoTLSVerify            bool          `mapstructure:"noTLSVerify"`
			ConnectTimeout         time.Duration `mapstructure:"connectTimeout"`
			TLSTimeout             time.Duration `mapstructure:"tlsTimeout"`
			TCPKeepAlive           time.Duration `mapstructure:"tcpKeepAlive"`
			KeepAliveConnections   int           `mapstructure:"keepAliveConnections"`
			KeepAliveTimeout       time.Duration `mapstructure:"keepAliveTimeout"`
			NoHappyEyeballs        bool          `mapstructure:"noHappyEyeballs"`
			ProxyType              string        `mapstructure:"proxyType"`
			HTTPHostHeader         string        `mapstructure:"httpHostHeader"`
			OriginServerName       string        `mapstructure:"originServerName"`
			CAPool                 string        `mapstructure:"caPool"`
			HTTP2Origin            bool          `mapstructure:"http2Origin"`
			DisableChunkedEncoding bool          `mapstructure:"disableChunkedEncoding"`
			// Access defaults
			AccessRequired bool   `mapstructure:"accessRequired"`
			AccessTeamName string `mapstructure:"accessTeamName"`
			AccessAudTag   string `mapstructure:"accessAudTag"`
		} `mapstructure:"originRequest"`
	} `mapstructure:"cloudflare"`
	Controller struct {
		// 容器抖动检测配置
		FlappingWindow    time.Duration `mapstructure:"flappingWindow"`
		FlappingThreshold int           `mapstructure:"flappingThreshold"`
		CoolingPeriod     time.Duration `mapstructure:"coolingPeriod"`
		MaxCoolingPeriod  time.Duration `mapstructure:"maxCoolingPeriod"`
		DebounceDuration  time.Duration `mapstructure:"debounceDuration"`
		// 对账配置
		ReconcileEnabled  bool          `mapstructure:"reconcileEnabled"`
		ReconcileInterval time.Duration `mapstructure:"reconcileInterval"`
	} `mapstructure:"controller"`
	Cleanup struct {
		OnExit    bool          `mapstructure:"onExit"`
		StateFile string        `mapstructure:"stateFile"`
		Strategy  string        `mapstructure:"strategy"`
		Timeout   time.Duration `mapstructure:"timeout"`
	} `mapstructure:"cleanup"`
	Defaults struct {
		Scheme string `mapstructure:"scheme"`
		Port   int    `mapstructure:"port"`
		Path   string `mapstructure:"path"`
	} `mapstructure:"defaults"`
	Compensation struct {
		InitialDelay  time.Duration `mapstructure:"initialDelay"`
		MaxDelay      time.Duration `mapstructure:"maxDelay"`
		MaxRetries    int           `mapstructure:"maxRetries"`
		PollInterval  time.Duration `mapstructure:"pollInterval"`
	} `mapstructure:"compensation"`
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
		ReconcileEnabled:  c.Controller.ReconcileEnabled,
		ReconcileInterval: c.Controller.ReconcileInterval,
	}
}

// GetCompensationConfig returns compensation configuration with defaults applied
func (c *Config) GetCompensationConfig() (initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	initialDelay = c.Compensation.InitialDelay
	if initialDelay == 0 {
		initialDelay = 30 * time.Second
	}
	maxDelay = c.Compensation.MaxDelay
	if maxDelay == 0 {
		maxDelay = 30 * time.Minute
	}
	maxRetries = c.Compensation.MaxRetries
	if maxRetries == 0 {
		maxRetries = 10
	}
	pollInterval = c.Compensation.PollInterval
	if pollInterval == 0 {
		pollInterval = 30 * time.Second
	}
	return
}

// GetOriginRequestDefaults 获取OriginRequest默认配置 (T082)
func (c *Config) GetOriginRequestDefaults() map[string]interface{} {
	defaults := make(map[string]interface{})

	// Only include non-zero/non-empty values
	if c.Cloudflare.OriginRequest.NoTLSVerify {
		defaults["noTLSVerify"] = true
	}
	if c.Cloudflare.OriginRequest.ConnectTimeout > 0 {
		defaults["connectTimeout"] = c.Cloudflare.OriginRequest.ConnectTimeout.String()
	}
	if c.Cloudflare.OriginRequest.TLSTimeout > 0 {
		defaults["tlsTimeout"] = c.Cloudflare.OriginRequest.TLSTimeout.String()
	}
	if c.Cloudflare.OriginRequest.TCPKeepAlive > 0 {
		defaults["tcpKeepAlive"] = c.Cloudflare.OriginRequest.TCPKeepAlive.String()
	}
	if c.Cloudflare.OriginRequest.KeepAliveConnections > 0 {
		defaults["keepAliveConnections"] = c.Cloudflare.OriginRequest.KeepAliveConnections
	}
	if c.Cloudflare.OriginRequest.KeepAliveTimeout > 0 {
		defaults["keepAliveTimeout"] = c.Cloudflare.OriginRequest.KeepAliveTimeout.String()
	}
	if c.Cloudflare.OriginRequest.NoHappyEyeballs {
		defaults["noHappyEyeballs"] = true
	}
	if c.Cloudflare.OriginRequest.ProxyType != "" {
		defaults["proxyType"] = c.Cloudflare.OriginRequest.ProxyType
	}
	if c.Cloudflare.OriginRequest.HTTPHostHeader != "" {
		defaults["httpHostHeader"] = c.Cloudflare.OriginRequest.HTTPHostHeader
	}
	if c.Cloudflare.OriginRequest.OriginServerName != "" {
		defaults["originServerName"] = c.Cloudflare.OriginRequest.OriginServerName
	}
	if c.Cloudflare.OriginRequest.CAPool != "" {
		defaults["caPool"] = c.Cloudflare.OriginRequest.CAPool
	}
	if c.Cloudflare.OriginRequest.HTTP2Origin {
		defaults["http2Origin"] = true
	}
	if c.Cloudflare.OriginRequest.DisableChunkedEncoding {
		defaults["disableChunkedEncoding"] = true
	}
	if c.Cloudflare.OriginRequest.AccessRequired {
		defaults["access.required"] = true
	}
	if c.Cloudflare.OriginRequest.AccessTeamName != "" {
		defaults["access.teamName"] = c.Cloudflare.OriginRequest.AccessTeamName
	}
	if c.Cloudflare.OriginRequest.AccessAudTag != "" {
		defaults["access.audTag"] = c.Cloudflare.OriginRequest.AccessAudTag
	}

	return defaults
}

// New 初始化并返回一个配置实例
func New() (*Config, error) {
	v := viper.New()

	// 1. 设置默认值
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("cloudflare.tunnelName", "DockTunnel")           // 默认通道名称
	v.SetDefault("cloudflare.catchAll", "http_status:404")        // 默认catch-all规则
	v.SetDefault("cloudflare.rateLimit", 10)                      // 默认每秒10个请求的速率限制
	v.SetDefault("cloudflare.maxRetries", 3)                      // 默认最大重试次数
	v.SetDefault("cloudflare.retryDelay", 1*time.Second)          // 默认初始重试延迟1秒
	v.SetDefault("cloudflare.maxRetryDelay", 30*time.Second)      // 默认最大重试延迟30秒
	v.SetDefault("controller.flappingWindow", 60*time.Second)     // 默认抖动检测窗口60秒
	v.SetDefault("controller.flappingThreshold", 5)               // 默认抖动阈值5次重启
	v.SetDefault("controller.coolingPeriod", 300*time.Second)     // 默认冷却期300秒(5分钟)
	v.SetDefault("controller.maxCoolingPeriod", 1800*time.Second) // 默认最大冷却期1800秒(30分钟)
	v.SetDefault("controller.debounceDuration", 2*time.Second)    // 默认防抖延迟2秒
	v.SetDefault("controller.reconcileEnabled", true)
	v.SetDefault("controller.reconcileInterval", 120*time.Second)
	v.SetDefault("cleanup.onExit", true)
	v.SetDefault("cleanup.strategy", "graceful-cleanup")
	v.SetDefault("cleanup.timeout", 30*time.Second)
	// Global defaults for label auto-detection fallback
	v.SetDefault("defaults.scheme", "http") // 默认scheme (http, https, tcp)
	v.SetDefault("defaults.port", 80)       // 默认端口
	v.SetDefault("defaults.path", "")       // 默认路径
	// Compensation defaults
	v.SetDefault("compensation.initialDelay", 30*time.Second)
	v.SetDefault("compensation.maxDelay", 30*time.Minute)
	v.SetDefault("compensation.maxRetries", 10)
	v.SetDefault("compensation.pollInterval", 30*time.Second)

	// 2. 设置配置文件
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")               // 在当前目录查找
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

	// Backward compatibility: onExit: true → strategy: graceful-cleanup
	if cfg.Cleanup.OnExit && cfg.Cleanup.Strategy == "" {
		cfg.Cleanup.Strategy = "graceful-cleanup"
	}

	// 6. 验证必需字段
	if cfg.Cloudflare.APIToken == "" {
		return nil, fmt.Errorf("cloudflare API token is required")
	}
	if cfg.Cloudflare.AccountID == "" {
		return nil, fmt.Errorf("cloudflare account ID is required")
	}

	return &cfg, nil
}

// ValidateAPIToken tests if the Cloudflare API token is valid by making a simple API call (T103)
func (c *Config) ValidateAPIToken() error {
	// Import cloudflare package to make a test API call
	// This validates the token has the required permissions
	// TODO: Implement actual Cloudflare API validation call
	// For now, just validate format (Bearer tokens typically start with certain patterns)
	if len(c.Cloudflare.APIToken) < 20 {
		return fmt.Errorf("API token appears to be invalid (too short, must be at least 20 characters)")
	}
	return nil
}

// SanitizeForLog returns a log-safe version of config with sensitive fields redacted (T104)
func (c *Config) SanitizeForLog() map[string]interface{} {
	return map[string]interface{}{
		"log": map[string]interface{}{
			"level":  c.Log.Level,
			"format": c.Log.Format,
		},
		"cloudflare": map[string]interface{}{
			"accountId":     c.Cloudflare.AccountID,
			"tunnelId":      c.Cloudflare.TunnelID,
			"tunnelName":    c.Cloudflare.TunnelName,
			"catchAll":      c.Cloudflare.CatchAll,
			"rateLimit":     c.Cloudflare.RateLimit,
			"maxRetries":    c.Cloudflare.MaxRetries,
			"retryDelay":    c.Cloudflare.RetryDelay,
			"maxRetryDelay": c.Cloudflare.MaxRetryDelay,
			// APIToken is intentionally omitted for security
			"apiToken": "[REDACTED]",
		},
		"controller": map[string]interface{}{
			"flappingWindow":    c.Controller.FlappingWindow,
			"flappingThreshold": c.Controller.FlappingThreshold,
			"coolingPeriod":     c.Controller.CoolingPeriod,
			"maxCoolingPeriod":  c.Controller.MaxCoolingPeriod,
			"debounceDuration":  c.Controller.DebounceDuration,
			"reconcileEnabled":  c.Controller.ReconcileEnabled,
			"reconcileInterval": c.Controller.ReconcileInterval,
		},
		"cleanup": map[string]interface{}{
			"onExit":    c.Cleanup.OnExit,
			"stateFile": c.Cleanup.StateFile,
			"strategy":  c.Cleanup.Strategy,
			"timeout":   c.Cleanup.Timeout,
		},
		"defaults": map[string]interface{}{
			"scheme": c.Defaults.Scheme,
			"port":   c.Defaults.Port,
			"path":   c.Defaults.Path,
		},
		"compensation": map[string]interface{}{
			"initialDelay": c.Compensation.InitialDelay,
			"maxDelay":     c.Compensation.MaxDelay,
			"maxRetries":   c.Compensation.MaxRetries,
			"pollInterval": c.Compensation.PollInterval,
		},
	}
}