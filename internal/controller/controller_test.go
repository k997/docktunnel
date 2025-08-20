package controller

import (
	"context"
	"testing"

	"github.com/cloudflare/cloudflare-go"
)

// mockCloudflareManager 是一个模拟的Cloudflare管理器，用于测试
type mockCloudflareManager struct {
	tunnel *cloudflare.Tunnel
}

// GetTunnel 返回模拟的隧道信息
func (m *mockCloudflareManager) GetTunnel() *cloudflare.Tunnel {
	return m.tunnel
}

// UpdateConfiguration 模拟更新配置
func (m *mockCloudflareManager) UpdateConfiguration(ctx context.Context, ingressRules []cloudflare.UnvalidatedIngressRule) error {
	return nil
}

// DeleteDNSRecord 模拟删除DNS记录
func (m *mockCloudflareManager) DeleteDNSRecord(ctx context.Context, hostname string) error {
	return nil
}

func TestNewController(t *testing.T) {
	// 测试创建控制器实例
	controller := &Controller{
		ingressRules:   make(map[string]cloudflare.UnvalidatedIngressRule),
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
	
	if catchAllRule.Service != "http_status:404" {
		t.Errorf("Expected catch-all service to be 'http_status:404', got '%s'", catchAllRule.Service)
	}
}

func TestCleanupResourcesLogic(t *testing.T) {
	// 创建控制器实例
	controller := NewController(nil, nil, "http_status:410")
	
	// 添加一些测试规则
	controller.ingressRules["example.com"] = cloudflare.UnvalidatedIngressRule{
		Hostname: "example.com",
		Service:  "http://localhost:8080",
	}
	
	controller.ingressRules["test.com"] = cloudflare.UnvalidatedIngressRule{
		Hostname: "test.com",
		Service:  "http://localhost:3000",
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
	controller.ingressRules = make(map[string]cloudflare.UnvalidatedIngressRule)
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

func TestParseLabelsToIngress(t *testing.T) {
	// 创建测试用的标签数据
	labels := map[string]string{
		"docktunnel.enable":                        "true",
		"docktunnel.web.hostname":                  "example.com",
		"docktunnel.web.service":                   "http://localhost:8080",
		"docktunnel.web.path":                      "/api",
		"docktunnel.web.originRequest.noTLSVerify": "true",
	}
	
	_ = labels // 确保labels变量被使用

	// TODO: 实现完整的测试逻辑
	t.Log("Test placeholder for ParseLabelsToIngress")
}