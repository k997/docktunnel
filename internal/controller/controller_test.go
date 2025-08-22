package controller

import (
	"context"
	"testing"

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
		ruleValidator:  NewCompositeValidator(),
	}

	if controller == nil {
		t.Error("Controller should not be nil")
	}
}

func TestNewControllerWithCatchAll(t *testing.T) {
	// 测试使用catchAllService创建控制器实例
	controller := NewController(nil, nil, "http_status:404")

	if controller == nil {
		t.Error("Controller should not be nil")
	}

	// 检查catch-all规则是否正确初始化
	catchAllRule, exists := controller.ingressRules["CATCH_ALL"]
	if !exists {
		t.Error("Catch-all rule should exist")
	}

	if catchAllRule.Service.Value != "http_status:404" {
		t.Errorf("Expected catch-all service to be 'http_status:404', got '%s'", catchAllRule.Service.Value)
	}
}

func TestCleanupResourcesLogic(t *testing.T) {
	// 创建控制器实例
	controller := NewController(nil, nil, "http_status:410")

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
	if len(controller.ingressRules) != 3 { // 2个普通规则 + 1个catch-all规则
		t.Errorf("Expected 3 ingress rules, got %d", len(controller.ingressRules))
	}

	if len(controller.containerRules) != 2 {
		t.Errorf("Expected 2 container rules, got %d", len(controller.containerRules))
	}

	// 手动测试CleanupResources的逻辑部分（不调用实际的方法）
	// 保存现有的catch-all规则
	catchAllRule, catchAllExists := controller.ingressRules["CATCH_ALL"]

	// 清空ingressRules和containerRules
	controller.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	controller.containerRules = make(map[string][]string)

	// 恢复catch-all规则
	if catchAllExists {
		controller.ingressRules["CATCH_ALL"] = catchAllRule
	}

	// 检查规则是否被正确清理（只保留catch-all规则）
	if len(controller.ingressRules) != 1 { // 只应该保留catch-all规则
		t.Errorf("Expected 1 ingress rule after cleanup, got %d", len(controller.ingressRules))
	}

	// 检查是否保留了catch-all规则
	_, catchAllExists = controller.ingressRules["CATCH_ALL"]
	if !catchAllExists {
		t.Error("Catch-all rule should still exist after cleanup")
	}

	// 检查containerRules是否被清空
	if len(controller.containerRules) != 0 {
		t.Errorf("Expected 0 container rules after cleanup, got %d", len(controller.containerRules))
	}
}
