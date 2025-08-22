package controller

import (
	"errors"
	"testing"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// mockRuleValidator 是一个模拟的规则验证器，用于测试
type mockRuleValidator struct {
	shouldError bool
}

func (m *mockRuleValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	if m.shouldError {
		return errors.New("validation error")
	}
	return nil
}

func TestParseLabelsToIngress_NoContainerInfo(t *testing.T) {
	// 测试containerInfo为nil的情况
	rules, err := parseLabelsToIngress(nil, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	if rules[0].Service.Value != "http_status:404" {
		t.Errorf("Expected service to be 'http_status:404', got %s", rules[0].Service.Value)
	}
}

func TestParseLabelsToIngress_NoConfig(t *testing.T) {
	// 测试containerInfo.Config为nil的情况
	containerInfo := &container.InspectResponse{}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	if rules[0].Service.Value != "http_status:404" {
		t.Errorf("Expected service to be 'http_status:404', got %s", rules[0].Service.Value)
	}
}

func TestParseLabelsToIngress_NoLabels(t *testing.T) {
	// 测试containerInfo.Config.Labels为nil的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	if rules[0].Service.Value != "http_status:404" {
		t.Errorf("Expected service to be 'http_status:404', got %s", rules[0].Service.Value)
	}
}

func TestParseLabelsToIngress_EmptyLabels(t *testing.T) {
	// 测试空标签的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	if rules[0].Service.Value != "http_status:404" {
		t.Errorf("Expected service to be 'http_status:404', got %s", rules[0].Service.Value)
	}
}

func TestParseLabelsToIngress_ServiceLabel(t *testing.T) {
	// 测试service标签的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有2个规则：1个服务规则 + 1个默认的catch-all规则
	if len(rules) != 2 {
		t.Errorf("Expected 2 rules, got %d", len(rules))
	}
	
	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			r := rule // 创建本地副本以避免循环变量问题
			webRule = &r
			break
		}
	}
	
	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}
	
	if webRule.Service.Value != "http://localhost:8080" {
		t.Errorf("Expected service to be 'http://localhost:8080', got %s", webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_PortProtoLabels(t *testing.T) {
	// 测试port和proto标签的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":      "true",
				"docktunnel.api.hostname": "api.example.com",
				"docktunnel.api.port":     "8080",
				"docktunnel.api.proto":    "https",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有2个规则：1个服务规则 + 1个默认的catch-all规则
	if len(rules) != 2 {
		t.Errorf("Expected 2 rules, got %d", len(rules))
	}
	
	// 查找api服务规则
	var apiRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "api.example.com" {
			r := rule // 创建本地副本以避免循环变量问题
			apiRule = &r
			break
		}
	}
	
	if apiRule == nil {
		t.Error("Expected to find api service rule")
		return
	}
	
	// 验证服务地址是否正确生成
	expectedService := "https://172.17.0.2:8080"
	if apiRule.Service.Value != expectedService {
		t.Errorf("Expected service to be '%s', got %s", expectedService, apiRule.Service.Value)
	}
}

func TestParseLabelsToIngress_PortOnlyLabel(t *testing.T) {
	// 测试只有port标签的情况（应该使用默认的http协议）
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.port":     "8080",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有2个规则：1个服务规则 + 1个默认的catch-all规则
	if len(rules) != 2 {
		t.Errorf("Expected 2 rules, got %d", len(rules))
	}
	
	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			r := rule // 创建本地副本以避免循环变量问题
			webRule = &r
			break
		}
	}
	
	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}
	
	// 验证服务地址是否正确生成（应该使用默认的http协议）
	expectedService := "http://172.17.0.2:8080"
	if webRule.Service.Value != expectedService {
		t.Errorf("Expected service to be '%s', got %s", expectedService, webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_ServiceOverridesPort(t *testing.T) {
	// 测试service标签应该覆盖port标签的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.service":  "http://external-service:3000",
				"docktunnel.web.port":     "8080",
				"docktunnel.web.proto":    "https",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有2个规则：1个服务规则 + 1个默认的catch-all规则
	if len(rules) != 2 {
		t.Errorf("Expected 2 rules, got %d", len(rules))
	}
	
	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			r := rule // 创建本地副本以避免循环变量问题
			webRule = &r
			break
		}
	}
	
	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}
	
	// 验证service标签的值被使用，而不是port/proto生成的值
	expectedService := "http://external-service:3000"
	if webRule.Service.Value != expectedService {
		t.Errorf("Expected service to be '%s', got %s", expectedService, webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_OriginRequestSettings(t *testing.T) {
	// 测试originRequest配置
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                            "true",
				"docktunnel.web.hostname":                      "example.com",
				"docktunnel.web.service":                       "http://localhost:8080",
				"docktunnel.web.originRequest.connectTimeout":  "30s",
				"docktunnel.web.originRequest.noTLSVerify":     "true",
				"docktunnel.web.originRequest.http2Origin":     "false",
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有2个规则：1个服务规则 + 1个默认的catch-all规则
	if len(rules) != 2 {
		t.Errorf("Expected 2 rules, got %d", len(rules))
	}
	
	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			r := rule // 创建本地副本以避免循环变量问题
			webRule = &r
			break
		}
	}
	
	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}
	
	if !webRule.OriginRequest.Present {
		t.Error("Expected OriginRequest to be set")
		return
	}
	
	// 验证connectTimeout设置
	if !webRule.OriginRequest.Value.ConnectTimeout.Present {
		t.Error("Expected ConnectTimeout to be set")
	} else if webRule.OriginRequest.Value.ConnectTimeout.Value != 30 {
		t.Errorf("Expected ConnectTimeout to be 30, got %v", webRule.OriginRequest.Value.ConnectTimeout.Value)
	}
	
	// 验证noTLSVerify设置
	if !webRule.OriginRequest.Value.NoTLSVerify.Present {
		t.Error("Expected NoTLSVerify to be set")
	} else if webRule.OriginRequest.Value.NoTLSVerify.Value != true {
		t.Errorf("Expected NoTLSVerify to be true, got %v", webRule.OriginRequest.Value.NoTLSVerify.Value)
	}
	
	// 验证http2Origin设置
	if !webRule.OriginRequest.Value.HTTP2Origin.Present {
		t.Error("Expected Http2Origin to be set")
	} else if webRule.OriginRequest.Value.HTTP2Origin.Value != false {
		t.Errorf("Expected Http2Origin to be false, got %v", webRule.OriginRequest.Value.HTTP2Origin.Value)
	}
}

func TestGetContainerIP(t *testing.T) {
	// 测试获取容器IP地址
	
	// 测试nil NetworkSettings
	containerInfo := &container.InspectResponse{}
	ip := getContainerIP(containerInfo)
	if ip != "" {
		t.Errorf("Expected empty IP, got %s", ip)
	}
	
	// 测试bridge网络
	containerInfo = &container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "172.17.0.2" {
		t.Errorf("Expected IP 172.17.0.2, got %s", ip)
	}
	
	// 测试其他网络
	containerInfo = &container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom": {
					IPAddress: "192.168.1.10",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "192.168.1.10" {
		t.Errorf("Expected IP 192.168.1.10, got %s", ip)
	}
	
	// 测试多个网络（应该优先选择bridge）
	containerInfo = &container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom": {
					IPAddress: "192.168.1.10",
				},
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "172.17.0.2" {
		t.Errorf("Expected IP 172.17.0.2 (bridge), got %s", ip)
	}
	
	// 测试空IP地址
	containerInfo = &container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "",
				},
				"custom": {
					IPAddress: "192.168.1.10",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "192.168.1.10" {
		t.Errorf("Expected IP 192.168.1.10, got %s", ip)
	}
}