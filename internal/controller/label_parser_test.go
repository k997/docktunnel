package controller

import (
	"errors"
	"testing"
	"time"

	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// mockRuleValidator 是一个模拟的规则验证器，用于测试
type mockRuleValidator struct {
	shouldError bool
}

func (m *mockRuleValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, allRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	if m.shouldError {
		return errors.New("validation error")
	}
	return nil
}

func TestParseLabelsToIngress_NoContainerInfo(t *testing.T) {
	// 测试containerInfo为nil的情况
	rules, err := parseLabelsToIngress(nil)
	
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
	rules, err := parseLabelsToIngress(containerInfo)
	
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
	rules, err := parseLabelsToIngress(containerInfo)
	
	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
	}
}

func TestParseLabelsToIngress_EmptyLabels(t *testing.T) {
	// 测试containerInfo.Config.Labels为空的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo)
	
	// 现在应该返回错误，因为没有有效的规则
	if err == nil {
		t.Error("Expected error for no valid rules, got none")
	}

	if len(rules) != 0 {
		t.Errorf("Expected 0 rules, got %d", len(rules))
	}
}

func TestParseLabelsToIngress_ServiceLabel(t *testing.T) {
	// 测试仅使用service标签的情况
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
	}
	rules, err := parseLabelsToIngress(containerInfo)
	
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
			webRule = rule
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
				"docktunnel.web.hostname": "example.com",
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
	rules, err := parseLabelsToIngress(containerInfo)
	
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
			webRule = rule
			break
		}
	}

	if webRule == nil {
		t.Error("Expected to find web service rule")
		return
	}

	// 默认协议应该是https
	if !webRule.Service.Present || webRule.Service.Value != "https://172.17.0.2:8080" {
		t.Errorf("Expected service to be 'https://172.17.0.2:8080', got '%s'", webRule.Service.Value)
	}
}

func TestParseLabelsToIngress_PortOnlyLabel(t *testing.T) {
	// 测试仅使用port标签的情况（默认使用http协议）
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
	rules, err := parseLabelsToIngress(containerInfo)
	
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
			webRule = rule
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
	rules, err := parseLabelsToIngress(containerInfo)
	
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
			webRule = rule
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
	rules, err := parseLabelsToIngress(containerInfo)
	
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
			apiRule = rule
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

func TestParseTraefikLabels_SingleHostname(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.web.rule":       "Host(`example.com`)",
		"traefik.http.routers.web.service":    "web-svc",
		"traefik.http.services.web-svc.loadbalancer.server.port": "8080",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "bridge",
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

	rules := parseTraefikLabels(labels, containerInfo)

	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule, got %d", len(rules))
	}

	rule, exists := rules["example.com"]
	if !exists {
		t.Fatalf("Expected rule for hostname 'example.com', not found")
	}

	if rule.Hostname.Value != "example.com" {
		t.Errorf("Expected hostname 'example.com', got '%s'", rule.Hostname.Value)
	}

	expectedService := "http://172.17.0.2:8080"
	if rule.Service.Value != expectedService {
		t.Errorf("Expected service '%s', got '%s'", expectedService, rule.Service.Value)
	}
}

func TestParseTraefikLabels_MultipleHostnames(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.web.rule":       "Host(`a.com`, `b.com`, `c.com`)",
		"traefik.http.routers.web.service":    "web-svc",
		"traefik.http.services.web-svc.loadbalancer.server.port": "8080",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "bridge",
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

	rules := parseTraefikLabels(labels, containerInfo)

	if len(rules) != 3 {
		t.Fatalf("Expected 3 rules, got %d", len(rules))
	}

	expectedHostnames := []string{"a.com", "b.com", "c.com"}
	for _, hostname := range expectedHostnames {
		if _, exists := rules[hostname]; !exists {
			t.Errorf("Expected rule for hostname '%s', not found", hostname)
		}
	}
}

func TestParseTraefikLabels_WithComplexRule(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.api.rule":       "Host(`api.example.com`) && Path(`/api`)",
		"traefik.http.routers.api.service":    "api-svc",
		"traefik.http.services.api-svc.loadbalancer.server.port": "9000",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "bridge",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.3",
				},
			},
		},
	}

	rules := parseTraefikLabels(labels, containerInfo)

	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule, got %d", len(rules))
	}

	rule, exists := rules["api.example.com"]
	if !exists {
		t.Fatalf("Expected rule for hostname 'api.example.com', not found")
	}

	if rule.Hostname.Value != "api.example.com" {
		t.Errorf("Expected hostname 'api.example.com', got '%s'", rule.Hostname.Value)
	}

	expectedService := "http://172.17.0.3:9000"
	if rule.Service.Value != expectedService {
		t.Errorf("Expected service '%s', got '%s'", expectedService, rule.Service.Value)
	}
}

func TestParseTraefikLabels_NoServiceName(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.web.rule": "Host(`example.com`)",
		// No service label - should use router name as service name
		"traefik.http.services.web.loadbalancer.server.port": "8080",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "bridge",
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

	rules := parseTraefikLabels(labels, containerInfo)

	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule, got %d", len(rules))
	}

	// Should still work because router name "web" matches service name "web"
	rule, exists := rules["example.com"]
	if !exists {
		t.Fatalf("Expected rule for hostname 'example.com', not found")
	}

	if rule.Service.Value != "http://172.17.0.2:8080" {
		t.Errorf("Expected service 'http://172.17.0.2:8080', got '%s'", rule.Service.Value)
	}
}

func TestParseTraefikLabels_DockTunnelTakesPrecedence(t *testing.T) {
	labels := map[string]string{
		// DockTunnel labels (highest priority)
		"docktunnel.enable":                        "true",
		"docktunnel.web.hostname":                  "docktunnel-example.com",
		"docktunnel.web.port":                      "8080",
		// Traefik labels (should be ignored when DockTunnel labels exist)
		"traefik.http.routers.web.rule":            "Host(`traefik-example.com`)",
		"traefik.http.services.web.loadbalancer.server.port": "9000",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "bridge",
			},
		},
		Config: &container.Config{
			Labels: labels,
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {
					IPAddress: "172.17.0.2",
				},
			},
		},
	}

	rules, err := parseLabelsToIngress(containerInfo)
	if err != nil {
		t.Fatalf("parseLabelsToIngress failed: %v", err)
	}

	// Should only have DockTunnel rule, not Traefik rule
	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule (from DockTunnel), got %d", len(rules))
	}

	// The rule key is the service name "web", not the hostname
	// Check that it's the DockTunnel hostname
	rule, exists := rules["web"]
	if !exists {
		t.Fatalf("Expected service name 'web', not found. Available keys: %v", getKeys(rules))
	}

	if rule.Hostname.Value != "docktunnel-example.com" {
		t.Errorf("Expected hostname 'docktunnel-example.com', got '%s'", rule.Hostname.Value)
	}

	// Verify the service URL is built from DockTunnel labels (port 8080, not 9000)
	expectedService := "http://172.17.0.2:8080"
	if rule.Service.Value != expectedService {
		t.Errorf("Expected service '%s', got '%s'", expectedService, rule.Service.Value)
	}
}

func getKeys(m map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestParseRetentionPolicy_Immediate tests parsing of immediate retention policy
func TestParseRetentionPolicy_Immediate(t *testing.T) {
	testCases := []string{"0", "immediate", "Immediate", " IMMEDIATE "}

	for _, tc := range testCases {
		policy, err := ParseRetentionPolicy(tc)
		if err != nil {
			t.Errorf("Expected no error for '%s', got: %v", tc, err)
			continue
		}
		if policy.Type != types.Immediate {
			t.Errorf("Expected Immediate type for '%s', got: %v", tc, policy.Type)
		}
	}
}

// TestParseRetentionPolicy_Forever tests parsing of forever retention policy
func TestParseRetentionPolicy_Forever(t *testing.T) {
	testCases := []string{"forever", "keep", "Keep", " FOREVER "}

	for _, tc := range testCases {
		policy, err := ParseRetentionPolicy(tc)
		if err != nil {
			t.Errorf("Expected no error for '%s', got: %v", tc, err)
			continue
		}
		if policy.Type != types.Forever {
			t.Errorf("Expected Forever type for '%s', got: %v", tc, policy.Type)
		}
	}
}

// TestParseRetentionPolicy_Timed tests parsing of timed retention policies
func TestParseRetentionPolicy_Timed(t *testing.T) {
	testCases := []struct {
		input    string
		expected time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h", 1 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
	}

	for _, tc := range testCases {
		policy, err := ParseRetentionPolicy(tc.input)
		if err != nil {
			t.Errorf("Expected no error for '%s', got: %v", tc.input, err)
			continue
		}
		if policy.Type != types.Timed {
			t.Errorf("Expected Timed type for '%s', got: %v", tc.input, policy.Type)
		}
		if policy.Duration != tc.expected {
			t.Errorf("Expected duration %v for '%s', got: %v", tc.expected, tc.input, policy.Duration)
		}
	}
}

// TestParseRetentionPolicy_Invalid tests parsing of invalid retention policy formats
func TestParseRetentionPolicy_Invalid(t *testing.T) {
	testCases := []string{"invalid", "xyz", "123", "-5m"}

	for _, tc := range testCases {
		_, err := ParseRetentionPolicy(tc)
		if err == nil {
			t.Errorf("Expected error for invalid value '%s', got nil", tc)
		}
	}
}

// TestParseLabelsToIngress_AccessConfig tests Cloudflare Access configuration parsing (T076, T078)
func TestParseLabelsToIngress_AccessConfig(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                      "true",
				"docktunnel.api.hostname":                "api.example.com",
				"docktunnel.api.service":                 "https://localhost:8443",
				"docktunnel.api.originRequest.access.required": "true",
				"docktunnel.api.originRequest.access.teamName":  "my-team",
				"docktunnel.api.originRequest.access.audTag":    "tag1, tag2, tag3",
			},
		},
	}

	rules, err := parseLabelsToIngress(containerInfo)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule, got %d", len(rules))
	}

	apiRule := rules["api"]
	if apiRule == nil {
		t.Fatal("Expected api rule to exist")
	}

	// Verify Access config is present and correctly parsed
	if !apiRule.OriginRequest.Present {
		t.Fatal("Expected OriginRequest to be present")
	}

	if !apiRule.OriginRequest.Value.Access.Present {
		t.Fatal("Expected Access config to be present")
	}

	access := apiRule.OriginRequest.Value.Access.Value

	// Verify required flag
	if !access.Required.Present || !access.Required.Value {
		t.Error("Expected Access.Required to be true")
	}

	// Verify team name
	if !access.TeamName.Present || access.TeamName.Value != "my-team" {
		t.Errorf("Expected TeamName to be 'my-team', got '%s'", access.TeamName.Value)
	}

	// Verify audience tags
	if !access.AUDTag.Present {
		t.Fatal("Expected AUDTag to be present")
	}

	expectedTags := []string{"tag1", "tag2", "tag3"}
	if len(access.AUDTag.Value) != len(expectedTags) {
		t.Fatalf("Expected %d tags, got %d", len(expectedTags), len(access.AUDTag.Value))
	}

	for i, tag := range access.AUDTag.Value {
		if tag != expectedTags[i] {
			t.Errorf("Expected tag[%d] to be '%s', got '%s'", i, expectedTags[i], tag)
		}
	}
}

// TestParseLabelsToIngress_AllOriginRequestAttributes tests all supported originRequest attributes (T076)
func TestParseLabelsToIngress_AllOriginRequestAttributes(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				NetworkMode: "default",
			},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                                "true",
				"docktunnel.web.hostname":                          "web.example.com",
				"docktunnel.web.service":                           "http://localhost:8080",
				"docktunnel.web.originRequest.noTLSVerify":         "true",
				"docktunnel.web.originRequest.connectTimeout":      "30s",
				"docktunnel.web.originRequest.tlsTimeout":          "10s",
				"docktunnel.web.originRequest.tcpKeepAlive":        "60s",
				"docktunnel.web.originRequest.keepAliveConnections": "100",
				"docktunnel.web.originRequest.keepAliveTimeout":    "90s",
				"docktunnel.web.originRequest.noHappyEyeballs":     "true",
				"docktunnel.web.originRequest.proxyType":           "socks",
				"docktunnel.web.originRequest.httpHostHeader":      "custom.host",
				"docktunnel.web.originRequest.originServerName":    "origin.example.com",
				"docktunnel.web.originRequest.caPool":              "/path/to/ca.pem",
				"docktunnel.web.originRequest.http2Origin":         "true",
				"docktunnel.web.originRequest.disableChunkedEncoding": "false",
			},
		},
	}

	rules, err := parseLabelsToIngress(containerInfo)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(rules) != 1 {
		t.Fatalf("Expected 1 rule, got %d", len(rules))
	}

	webRule := rules["web"]
	if webRule == nil {
		t.Fatal("Expected web rule to exist")
	}

	// Verify OriginRequest config is present
	if !webRule.OriginRequest.Present {
		t.Fatal("Expected OriginRequest to be present")
	}

	originRequest := webRule.OriginRequest.Value

	// Verify all boolean attributes
	if !originRequest.NoTLSVerify.Present || !originRequest.NoTLSVerify.Value {
		t.Error("Expected NoTLSVerify to be true")
	}
	if !originRequest.NoHappyEyeballs.Present || !originRequest.NoHappyEyeballs.Value {
		t.Error("Expected NoHappyEyeballs to be true")
	}
	if !originRequest.HTTP2Origin.Present || !originRequest.HTTP2Origin.Value {
		t.Error("Expected HTTP2Origin to be true")
	}
	if !originRequest.DisableChunkedEncoding.Present || originRequest.DisableChunkedEncoding.Value {
		t.Error("Expected DisableChunkedEncoding to be false")
	}

	// Verify all string attributes
	if !originRequest.ProxyType.Present || originRequest.ProxyType.Value != "socks" {
		t.Errorf("Expected ProxyType to be 'socks', got '%s'", originRequest.ProxyType.Value)
	}
	if !originRequest.HTTPHostHeader.Present || originRequest.HTTPHostHeader.Value != "custom.host" {
		t.Errorf("Expected HTTPHostHeader to be 'custom.host', got '%s'", originRequest.HTTPHostHeader.Value)
	}
	if !originRequest.OriginServerName.Present || originRequest.OriginServerName.Value != "origin.example.com" {
		t.Errorf("Expected OriginServerName to be 'origin.example.com', got '%s'", originRequest.OriginServerName.Value)
	}
	if !originRequest.CAPool.Present || originRequest.CAPool.Value != "/path/to/ca.pem" {
		t.Errorf("Expected CAPool to be '/path/to/ca.pem', got '%s'", originRequest.CAPool.Value)
	}

	// Verify all integer attributes
	if !originRequest.KeepAliveConnections.Present || originRequest.KeepAliveConnections.Value != 100 {
		t.Errorf("Expected KeepAliveConnections to be 100, got %d", originRequest.KeepAliveConnections.Value)
	}

	t.Log("All originRequest attributes parsed successfully")
}

// TestSanitizeLabelValue tests label value sanitization for injection prevention (T102)
func TestSanitizeLabelValue(t *testing.T) {
	testCases := []struct {
		name      string
		label     string
		value     string
		wantValid bool
	}{
		{
			name:      "valid hostname",
			label:     "docktunnel.web.hostname",
			value:     "example.com",
			wantValid: true,
		},
		{
			name:      "valid service URL",
			label:     "docktunnel.web.service",
			value:     "http://localhost:8080",
			wantValid: true,
		},
		{
			name:      "semicolon injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com; rm -rf /",
			wantValid: false,
		},
		{
			name:      "ampersand injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com & malicious",
			wantValid: false,
		},
		{
			name:      "pipe injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com | cat",
			wantValid: false,
		},
		{
			name:      "backtick injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com`whoami`",
			wantValid: false,
		},
		{
			name:      "dollar sign injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com$(malicious)",
			wantValid: false,
		},
		{
			name:      "single quote injection",
			label:     "docktunnel.web.hostname",
			value:     "example.com' OR '1'='1",
			wantValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeLabelValue(tc.label, tc.value)
			if result != tc.wantValid {
				t.Errorf("sanitizeLabelValue() = %v, want %v", result, tc.wantValid)
			}
		})
	}
}
