package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		InitialDelay time.Duration `mapstructure:"initialDelay"`
		MaxDelay     time.Duration `mapstructure:"maxDelay"`
		MaxRetries   int           `mapstructure:"maxRetries"`
		PollInterval time.Duration `mapstructure:"pollInterval"`
		MaxQueueSize int           `mapstructure:"maxQueueSize"`
	} `mapstructure:"compensation"`
	Persistence struct {
		BackupCount    int  `mapstructure:"backupCount"`
		ValidateOnLoad bool `mapstructure:"validateOnLoad"`
	} `mapstructure:"persistence"`
	Server struct {
		BindAddr   string `mapstructure:"bindAddr"`
		Port       int    `mapstructure:"port"`
		DebugToken string `mapstructure:"debugToken"`
	} `mapstructure:"server"`
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

// GetCompensationQueueCap returns the maximum compensation queue size with default applied.
func (c *Config) GetCompensationQueueCap() int {
	if c.Compensation.MaxQueueSize <= 0 {
		return 1000
	}
	return c.Compensation.MaxQueueSize
}

// GetPersistenceConfig returns persistence configuration with defaults applied.
func (c *Config) GetPersistenceConfig() (backupCount int, validateOnLoad bool) {
	backupCount = c.Persistence.BackupCount
	if backupCount == 0 {
		backupCount = 3
	}
	validateOnLoad = c.Persistence.ValidateOnLoad
	if !validateOnLoad {
		validateOnLoad = true
	}
	return backupCount, validateOnLoad
}

// GetServerAddr returns the configured "host:port" for the diagnostics/metrics server.
func (c *Config) GetServerAddr() string {
	addr := c.Server.BindAddr
	if addr == "" {
		addr = "127.0.0.1"
	}
	port := c.Server.Port
	if port == 0 {
		port = 9100
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

// isLoopbackBind reports whether the bind address will only listen on the
// local loopback interface. Empty defaults to loopback. Wildcards like
// "0.0.0.0" or "::" are NOT loopback.
func isLoopbackBind(addr string) bool {
	switch addr {
	case "", "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// GetOriginRequestDefaults 获取OriginRequest默认配置 (T082)
func (c *Config) GetOriginRequestDefaults() map[string]any {
	defaults := make(map[string]any)

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
	// Default to false: tearing down DNS records on every restart/crash makes
	// external hostnames briefly unreachable. Operators should opt in.
	v.SetDefault("cleanup.onExit", false)
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
	v.SetDefault("compensation.maxQueueSize", 1000)
	// Persistence defaults
	v.SetDefault("persistence.backupCount", 3)
	v.SetDefault("persistence.validateOnLoad", true)

	// Server defaults
	v.SetDefault("server.bindAddr", "127.0.0.1")
	v.SetDefault("server.port", 9100)

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

	// 7. 范围校验：负值或零值会让 retry / debounce / reconcile 等运行时路径行为异常
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validate enforces positive/non-zero values on operational fields.
// Viper silently drops malformed duration strings to zero, which would otherwise
// bypass debounce, disable cooling periods, or cause tight reconcile loops.
func (c *Config) validate() error {
	var errs []string

	add := func(cond bool, msg string) {
		if cond {
			errs = append(errs, msg)
		}
	}

	// Cloudflare retry/rate
	add(c.Cloudflare.RateLimit < 0, "cloudflare.rateLimit must be >= 0 (0 disables rate limiting)")
	add(c.Cloudflare.MaxRetries < 0, "cloudflare.maxRetries must be >= 0")
	add(c.Cloudflare.RetryDelay < 0, "cloudflare.retryDelay must be >= 0")
	add(c.Cloudflare.MaxRetryDelay < 0, "cloudflare.maxRetryDelay must be >= 0")
	add(c.Cloudflare.RetryDelay > 0 && c.Cloudflare.MaxRetryDelay > 0 && c.Cloudflare.RetryDelay > c.Cloudflare.MaxRetryDelay,
		"cloudflare.retryDelay must not exceed cloudflare.maxRetryDelay")

	// Controller timing
	add(c.Controller.FlappingWindow < 0, "controller.flappingWindow must be >= 0")
	add(c.Controller.FlappingThreshold < 0, "controller.flappingThreshold must be >= 0")
	add(c.Controller.CoolingPeriod < 0, "controller.coolingPeriod must be >= 0")
	add(c.Controller.MaxCoolingPeriod < 0, "controller.maxCoolingPeriod must be >= 0")
	add(c.Controller.CoolingPeriod > 0 && c.Controller.MaxCoolingPeriod > 0 && c.Controller.CoolingPeriod > c.Controller.MaxCoolingPeriod,
		"controller.coolingPeriod must not exceed controller.maxCoolingPeriod")
	add(c.Controller.DebounceDuration < 0, "controller.debounceDuration must be >= 0")
	add(c.Controller.ReconcileInterval <= 0 && c.Controller.ReconcileEnabled,
		"controller.reconcileInterval must be > 0 when reconcile is enabled")

	// Cleanup
	add(c.Cleanup.Timeout < 0, "cleanup.timeout must be >= 0")
	switch c.Cleanup.Strategy {
	case "", "graceful-cleanup", "force-cleanup", "none":
	default:
		errs = append(errs, fmt.Sprintf("cleanup.strategy %q is not recognized (expected graceful-cleanup|force-cleanup|none)", c.Cleanup.Strategy))
	}

	// Compensation
	add(c.Compensation.InitialDelay < 0, "compensation.initialDelay must be >= 0")
	add(c.Compensation.MaxDelay < 0, "compensation.maxDelay must be >= 0")
	add(c.Compensation.MaxRetries < 0, "compensation.maxRetries must be >= 0")
	add(c.Compensation.PollInterval <= 0 && (c.Compensation.InitialDelay > 0 || c.Compensation.MaxDelay > 0),
		"compensation.pollInterval must be > 0 when compensation is configured")
	add(c.Compensation.MaxQueueSize < 0, "compensation.maxQueueSize must be >= 0")

	// Server
	add(c.Server.Port < 0 || c.Server.Port > 65535, "server.port must be in [0,65535]")
	// /debug/state leaks all hostnames and service URLs. Refuse to expose it
	// on a non-loopback interface without a bearer token gating access.
	if !isLoopbackBind(c.Server.BindAddr) && c.Server.DebugToken == "" {
		errs = append(errs, "server.debugToken is required when server.bindAddr is not a loopback address (e.g. 127.0.0.1 / ::1 / localhost)")
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
}

// ValidateAPIToken performs a cheap format sanity check on the token.
// It does NOT verify the token against Cloudflare. Use VerifyAPIToken for that.
func (c *Config) ValidateAPIToken() error {
	if c.Cloudflare.APIToken == "" {
		return fmt.Errorf("API token is empty")
	}
	if len(c.Cloudflare.APIToken) < 20 {
		return fmt.Errorf("API token appears to be invalid (too short, must be at least 20 characters)")
	}
	return nil
}

// APITokenVerifier performs the actual /user/tokens/verify call against Cloudflare.
// Returns nil if the token is valid, an error otherwise.
type APITokenVerifier func(ctx context.Context, token string) error

// VerifyAPIToken validates the configured token against Cloudflare's
// /user/tokens/verify endpoint. Pass nil verifier to use DefaultAPITokenVerifier.
func (c *Config) VerifyAPIToken(ctx context.Context, verifier APITokenVerifier) error {
	if err := c.ValidateAPIToken(); err != nil {
		return err
	}
	if verifier == nil {
		verifier = DefaultAPITokenVerifier
	}
	return verifier(ctx, c.Cloudflare.APIToken)
}

// DefaultAPITokenVerifier is the production verifier that hits Cloudflare's API.
// It checks both the HTTP status (200) and the JSON "success" flag, since
// Cloudflare returns 200 with success=false on bad tokens.
var DefaultAPITokenVerifier APITokenVerifier = func(ctx context.Context, token string) error {
	const verifyURL = "https://api.cloudflare.com/client/v4/user/tokens/verify"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
	if err != nil {
		return fmt.Errorf("build verify request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("token verify request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("read verify response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token verify failed (status %d): %s", resp.StatusCode, truncate(string(body), 256))
	}

	var parsed struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("parse verify response: %w", err)
	}
	if !parsed.Success {
		msg := "unknown error"
		if len(parsed.Errors) > 0 {
			msg = parsed.Errors[0].Message
		}
		return fmt.Errorf("cloudflare rejected token: %s", msg)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// SanitizeForLog returns a log-safe version of config with sensitive fields redacted (T104)
func (c *Config) SanitizeForLog() map[string]any {
	return map[string]any{
		"log": map[string]any{
			"level":  c.Log.Level,
			"format": c.Log.Format,
		},
		"cloudflare": map[string]any{
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
		"controller": map[string]any{
			"flappingWindow":    c.Controller.FlappingWindow,
			"flappingThreshold": c.Controller.FlappingThreshold,
			"coolingPeriod":     c.Controller.CoolingPeriod,
			"maxCoolingPeriod":  c.Controller.MaxCoolingPeriod,
			"debounceDuration":  c.Controller.DebounceDuration,
			"reconcileEnabled":  c.Controller.ReconcileEnabled,
			"reconcileInterval": c.Controller.ReconcileInterval,
		},
		"cleanup": map[string]any{
			"onExit":    c.Cleanup.OnExit,
			"stateFile": c.Cleanup.StateFile,
			"strategy":  c.Cleanup.Strategy,
			"timeout":   c.Cleanup.Timeout,
		},
		"defaults": map[string]any{
			"scheme": c.Defaults.Scheme,
			"port":   c.Defaults.Port,
			"path":   c.Defaults.Path,
		},
		"compensation": map[string]any{
			"initialDelay": c.Compensation.InitialDelay,
			"maxDelay":     c.Compensation.MaxDelay,
			"maxRetries":   c.Compensation.MaxRetries,
			"pollInterval": c.Compensation.PollInterval,
			"maxQueueSize": c.Compensation.MaxQueueSize,
		},
		"persistence": map[string]any{
			"backupCount":    c.Persistence.BackupCount,
			"validateOnLoad": c.Persistence.ValidateOnLoad,
		},
		"server": map[string]any{
			"bindAddr": c.Server.BindAddr,
			"port":     c.Server.Port,
		},
	}
}
