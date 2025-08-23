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

	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
	}
}

func TestParseLabelsToIngress_NoConfig(t *testing.T) {
	// 测试containerInfo.Config为nil的情况
	containerInfo := &container.InspectResponse{}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})

	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
	}
}

func TestParseLabelsToIngress_NoLabels(t *testing.T) {
	// 测试containerInfo.Config.Labels为nil的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})

	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
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

	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
	}
}

func TestParseLabelsToIngress_ServiceLabel(t *testing.T) {
	// 测试使用service标签的情况
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
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

	// 应该有1个规则：1个服务规则（不再添加默认的catch-all规则）
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
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

	if !webRule.Service.Present || webRule.Service.Value != "http://localhost:8080" {
		t.Errorf("Expected service to be 'http://localhost:8080', got '%s'", webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_PortProtoLabels(t *testing.T) {
	// 测试使用port和proto标签的情况
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
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

	// 应该有1个规则：1个服务规则（不再添加默认的catch-all规则）
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}

	// 查找api服务规则
	var apiRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "api.example.com" {
			apiRule = &rule
			break
		}
	}

	if apiRule == nil {
		t.Error("Expected to find api service rule")
		return
	}

	if !apiRule.Service.Present || apiRule.Service.Value != "https://172.17.0.2:8080" {
		t.Errorf("Expected service to be 'https://172.17.0.2:8080', got '%s'", apiRule.Service.Value)
	}
}

func TestParseLabelsToIngress_PortOnlyLabel(t *testing.T) {
	// 测试只使用port标签的情况
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
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

	// 应该有1个规则：1个服务规则（不再添加默认的catch-all规则）
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}

	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			webRule = &rule
			break
		}
	}

	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}

	// 默认协议应该是http
	if !webRule.Service.Present || webRule.Service.Value != "http://172.17.0.2:8080" {
		t.Errorf("Expected service to be 'http://172.17.0.2:8080', got '%s'", webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_ServiceOverridesPort(t *testing.T) {
	// 测试service标签覆盖port和proto标签的情况
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
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
	
	// 应该有1个规则：1个服务规则（不再添加默认的catch-all规则）
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	// 查找web服务规则
	var webRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "example.com" {
			webRule = &rule
			break
		}
	}
	
	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}
	
	// service标签应该覆盖port和proto标签
	if !webRule.Service.Present || webRule.Service.Value != "http://external-service:3000" {
		t.Errorf("Expected service to be 'http://external-service:3000', got '%s'", webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_OriginRequestSettings(t *testing.T) {
	// 测试origin request设置
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                           "true",
				"docktunnel.api.hostname":                     "api.example.com",
				"docktunnel.api.service":                      "https://localhost:8443",
				"docktunnel.api.originRequest.connectTimeout": "5s",
				"docktunnel.api.originRequest.noTLSVerify":    "true",
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo, &mockRuleValidator{})
	
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 应该有1个规则：1个服务规则（不再添加默认的catch-all规则）
	if len(rules) != 1 {
		t.Errorf("Expected 1 rule, got %d", len(rules))
	}
	
	// 查找api服务规则
	var apiRule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rules {
		if rule.Hostname.Present && rule.Hostname.Value == "api.example.com" {
			apiRule = &rule
			break
		}
	}
	
	if apiRule == nil {
		t.Error("Expected to find api service rule")
		return
	}
	
	if !apiRule.Service.Present || apiRule.Service.Value != "https://localhost:8443" {
		t.Errorf("Expected service to be 'https://localhost:8443', got '%s'", apiRule.Service.Value)
	}
	
	if !apiRule.OriginRequest.Present {
		t.Error("Expected OriginRequest to be present")
		return
	}
	
	if !apiRule.OriginRequest.Value.ConnectTimeout.Present || apiRule.OriginRequest.Value.ConnectTimeout.Value != 5 {
		t.Errorf("Expected connect timeout to be 5, got %d", apiRule.OriginRequest.Value.ConnectTimeout.Value)
	}
	
	if !apiRule.OriginRequest.Value.NoTLSVerify.Present || !apiRule.OriginRequest.Value.NoTLSVerify.Value {
		t.Error("Expected NoTLSVerify to be true")
	}
}

func TestGetContainerIP(t *testing.T) {
	// 测试获取容器IP地址

	// 测试nil NetworkSettings
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
	}
	ip := getContainerIP(containerInfo)
	if ip != "" {
		t.Errorf("Expected empty IP, got %s", ip)
	}

	// 测试bridge网络
	containerInfo = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
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
	ip = getContainerIP(containerInfo)
	if ip != "172.17.0.2" {
		t.Errorf("Expected IP 172.17.0.2, got %s", ip)
	}

	// 测试其他网络
	containerInfo = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom_network": {
					IPAddress: "192.168.1.10",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "192.168.1.10" {
		t.Errorf("Expected IP 192.168.1.10, got %s", ip)
	}

	// 测试host网络模式
	containerInfo = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "host",
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
	ip = getContainerIP(containerInfo)
	if ip != "localhost" {
		t.Errorf("Expected localhost for host network mode, got %s", ip)
	}

	// 测试没有IP地址的情况
	containerInfo = &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "",
				},
			},
		},
	}
	ip = getContainerIP(containerInfo)
	if ip != "" {
		t.Errorf("Expected empty IP, got %s", ip)
	}
}
