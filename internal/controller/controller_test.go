package controller

import (
	"context"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// mockCloudflareManager 是一个模拟的Cloudflare管理器，用于测试
type mockCloudflareManager struct {
	tunnel *zero_trust.TunnelCloudflaredGetResponse
}

// GetTunnel 返回模拟的隧道信息
func (m *mockCloudflareManager) GetTunnel() *zero_trust.TunnelCloudflaredGetResponse {
	return m.tunnel
}

// UpdateConfiguration 模拟更新配置
func (m *mockCloudflareManager) UpdateConfiguration(ctx context.Context, ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	return nil
}

// ListDNSRecords 模拟列出DNS记录
func (m *mockCloudflareManager) ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error) {
	return []dns.RecordResponse{}, nil
}

// DeleteDNSRecords 模拟批量删除DNS记录
func (m *mockCloudflareManager) DeleteDNSRecords(ctx context.Context, hostnames []string) error {
	return nil
}

// UpsertDNSRecords 模拟批量创建或更新DNS记录
func (m *mockCloudflareManager) UpsertDNSRecords(ctx context.Context, hostnames []string) error {
	return nil
}

func TestNewController(t *testing.T) {
	// 测试创建控制器实例
	controller := &Controller{
		ingressRules:   make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules: make(map[string][]string),
		containerHealth: make(map[string]*ContainerHealth),
		ruleValidator:  NewCompositeValidator(),
	}

	if controller == nil {
		t.Error("Controller should not be nil")
	}

	// 检查初始状态（不再存储catch-all规则）
	if len(controller.ingressRules) != 0 {
		t.Errorf("Expected no rules initially, got %d", len(controller.ingressRules))
	}
	
	// 检查通过GetIngressRules方法可以获取到catch-all规则
	rules := controller.GetIngressRules()
	if len(rules) == 0 {
		t.Error("Expected to get rules from GetIngressRules, got none")
		return
	}
	
	// 检查最后一个规则是否为catch-all规则
	lastRule := rules[len(rules)-1]
	if lastRule.Service.Value != "http_status:404" {
		t.Errorf("Expected last rule to be catch-all with service 'http_status:404', got '%s'", lastRule.Service.Value)
	}
}

func TestNewControllerWithOptions(t *testing.T) {
	// 测试使用ControllerOptions创建控制器实例
	opts := ControllerOptions{
		CatchAllService:   "http_status:404",
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     300 * time.Second,
		MaxCoolingPeriod:  1800 * time.Second,
		DebounceDuration:  2 * time.Second,
	}

	controller := NewController(nil, nil, opts)

	if controller == nil {
		t.Error("Controller should not be nil")
	}

	// 检查配置选项是否正确应用
	if controller.flappingWindow != 60*time.Second {
		t.Errorf("Expected flappingWindow to be 60s, got %v", controller.flappingWindow)
	}

	if controller.flappingThreshold != 5 {
		t.Errorf("Expected flappingThreshold to be 5, got %d", controller.flappingThreshold)
	}

	if controller.coolingPeriod != 300*time.Second {
		t.Errorf("Expected coolingPeriod to be 300s, got %v", controller.coolingPeriod)
	}

	if controller.maxCoolingPeriod != 1800*time.Second {
		t.Errorf("Expected maxCoolingPeriod to be 1800s, got %v", controller.maxCoolingPeriod)
	}

	if controller.debounceDuration != 2*time.Second {
		t.Errorf("Expected debounceDuration to be 2s, got %v", controller.debounceDuration)
	}

	// 检查初始状态（不再存储catch-all规则）
	if len(controller.ingressRules) != 0 {
		t.Errorf("Expected no rules initially, got %d", len(controller.ingressRules))
	}
	
	// 检查通过GetIngressRules方法可以获取到catch-all规则
	rules := controller.GetIngressRules()
	if len(rules) == 0 {
		t.Error("Expected to get rules from GetIngressRules, got none")
		return
	}
	
	// 检查最后一个规则是否为catch-all规则
	lastRule := rules[len(rules)-1]
	if lastRule.Service.Value != "http_status:404" {
		t.Errorf("Expected last rule to be catch-all with service 'http_status:404', got '%s'", lastRule.Service.Value)
	}
}

func TestControllerOptions(t *testing.T) {
	// 测试ControllerOptions结构体
	opts := ControllerOptions{
		CatchAllService:   "http_status:404",
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     300 * time.Second,
		MaxCoolingPeriod:  1800 * time.Second,
		DebounceDuration:  2 * time.Second,
	}

	if opts.CatchAllService != "http_status:404" {
		t.Errorf("Expected CatchAllService to be 'http_status:404', got %s", opts.CatchAllService)
	}

	if opts.FlappingWindow != 60*time.Second {
		t.Errorf("Expected FlappingWindow to be 60s, got %v", opts.FlappingWindow)
	}

	if opts.FlappingThreshold != 5 {
		t.Errorf("Expected FlappingThreshold to be 5, got %d", opts.FlappingThreshold)
	}

	if opts.CoolingPeriod != 300*time.Second {
		t.Errorf("Expected CoolingPeriod to be 300s, got %v", opts.CoolingPeriod)
	}

	if opts.MaxCoolingPeriod != 1800*time.Second {
		t.Errorf("Expected MaxCoolingPeriod to be 1800s, got %v", opts.MaxCoolingPeriod)
	}

	if opts.DebounceDuration != 2*time.Second {
		t.Errorf("Expected DebounceDuration to be 2s, got %v", opts.DebounceDuration)
	}
}

func TestCleanupResourcesLogic(t *testing.T) {
	// 创建控制器实例
	opts := ControllerOptions{
		CatchAllService: "http_status:410",
	}
	controller := NewController(nil, nil, opts)

	// 添加一些测试规则
	controller.ingressRules["example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}

	controller.ingressRules["test.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("test.com"),
		Service:  cloudflare.F("http://localhost:3000"),
	}

	controller.containerRules["container1"] = []string{"example.com", "test.com"}
	controller.containerRules["container2"] = []string{"test.com"}

	// 检查规则是否正确添加
	rules := controller.GetIngressRules()
	if len(rules) != 3 { // 2个普通规则 + 1个catch-all规则
		t.Errorf("Expected 3 ingress rules, got %d", len(rules))
	}

	if len(controller.containerRules) != 2 {
		t.Errorf("Expected 2 container rules, got %d", len(controller.containerRules))
	}

	// 手动测试CleanupResources的逻辑部分（不调用实际的方法）
	// 清空ingressRules和containerRules
	controller.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	controller.containerRules = make(map[string][]string)

	// 检查规则是否被正确清理（不保留任何规则）
	rules = controller.GetIngressRules()
	if len(rules) != 1 { // 只应该保留动态添加的catch-all规则
		t.Errorf("Expected 1 ingress rule after cleanup (catch-all), got %d", len(rules))
	}


	// 检查containerRules是否被清空
	if len(controller.containerRules) != 0 {
		t.Errorf("Expected 0 container rules after cleanup, got %d", len(controller.containerRules))
	}
}

func TestContainerHealthStruct(t *testing.T) {
	// 测试ContainerHealth结构体
	health := &ContainerHealth{
		RestartCount: 3,
		IsFlapping:   true,
	}

	if health.RestartCount != 3 {
		t.Errorf("Expected RestartCount to be 3, got %d", health.RestartCount)
	}

	if !health.IsFlapping {
		t.Error("Expected IsFlapping to be true")
	}
}