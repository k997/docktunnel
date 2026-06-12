package label

import (
	"fmt"
	"testing"
	"time"

	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestParse_NilContainerInfo(t *testing.T) {
	_, err := Parse(nil)
	if err == nil {
		t.Error("expected error for nil container info")
	}
}

func TestParse_NoLabels(t *testing.T) {
	containerInfo := &container.InspectResponse{
		Config: &container.Config{Labels: map[string]string{}},
	}
	_, err := Parse(containerInfo)
	if err == nil {
		t.Error("expected error for no labels")
	}
}

func TestParse_DockTunnelOnly(t *testing.T) {
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	web := rules["web"]
	if web == nil {
		t.Fatal("expected 'web' rule")
	}
	if web.Hostname.Value != "example.com" {
		t.Errorf("expected hostname 'example.com', got '%s'", web.Hostname.Value)
	}
	if web.Service.Value != "http://localhost:8080" {
		t.Errorf("expected service 'http://localhost:8080', got '%s'", web.Service.Value)
	}
}

func TestParse_TraefikOnly(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                                    "true",
				"traefik.http.routers.myapp.rule":                      "Host(`app.example.com`)",
				"traefik.http.services.myapp.loadbalancer.server.port":  "8080",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	spec := rules["myapp@app.example.com"]
	if spec == nil {
		t.Fatalf("expected 'myapp@app.example.com' rule, got keys: %v", ruleKeys(rules))
	}
	if spec.Hostname.Value != "app.example.com" {
		t.Errorf("expected hostname 'app.example.com', got '%s'", spec.Hostname.Value)
	}
	if spec.Service.Value != "http://172.17.0.2:8080" {
		t.Errorf("expected service 'http://172.17.0.2:8080', got '%s'", spec.Service.Value)
	}
}

func TestParse_BothLabels_DockTunnelOverrides(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                                    "true",
				"docktunnel.web.hostname":                              "example.com",
				"docktunnel.web.service":                               "http://localhost:3000",
				"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
				"traefik.http.services.myapp.loadbalancer.server.port":  "8080",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	web := rules["web"]
	if web == nil {
		t.Fatal("expected 'web' rule from docktunnel")
	}
	if web.Service.Value != "http://localhost:3000" {
		t.Errorf("expected docktunnel service 'http://localhost:3000', got '%s'", web.Service.Value)
	}

	if _, ok := rules["myapp@example.com"]; ok {
		t.Error("traefik entry should be removed when docktunnel has same hostname")
	}
}

func TestParse_BothLabels_NoConflict(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                                    "true",
				"docktunnel.web.hostname":                              "web.example.com",
				"docktunnel.web.service":                               "http://localhost:8080",
				"traefik.http.routers.api.rule":                        "Host(`api.example.com`)",
				"traefik.http.services.api.loadbalancer.server.port":    "3000",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
}

func TestParse_PortWithAutoDetect(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "example.com",
				"docktunnel.web.port":     "8080",
				"docktunnel.web.scheme":   "https",
			},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	web := rules["web"]
	if web.Service.Value != "https://172.17.0.2:8080" {
		t.Errorf("expected 'https://172.17.0.2:8080', got '%s'", web.Service.Value)
	}
}

func TestParseRetentionPolicy_Immediate(t *testing.T) {
	for _, input := range []string{"0", "immediate", "Immediate", " IMMEDIATE "} {
		policy, err := ParseRetentionPolicy(input)
		if err != nil {
			t.Errorf("unexpected error for '%s': %v", input, err)
			continue
		}
		if policy.Type != types.Immediate {
			t.Errorf("expected Immediate for '%s', got %v", input, policy.Type)
		}
	}
}

func TestParseRetentionPolicy_Forever(t *testing.T) {
	for _, input := range []string{"forever", "keep", "Keep", " FOREVER "} {
		policy, err := ParseRetentionPolicy(input)
		if err != nil {
			t.Errorf("unexpected error for '%s': %v", input, err)
			continue
		}
		if policy.Type != types.Forever {
			t.Errorf("expected Forever for '%s', got %v", input, policy.Type)
		}
	}
}

func TestParseRetentionPolicy_Timed(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h", 1 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
	}

	for _, tt := range tests {
		policy, err := ParseRetentionPolicy(tt.input)
		if err != nil {
			t.Errorf("unexpected error for '%s': %v", tt.input, err)
			continue
		}
		if policy.Type != types.Timed {
			t.Errorf("expected Timed for '%s', got %v", tt.input, policy.Type)
		}
		if policy.Duration != tt.expected {
			t.Errorf("expected %v for '%s', got %v", tt.expected, tt.input, policy.Duration)
		}
	}
}

func TestParseRetentionPolicy_Invalid(t *testing.T) {
	for _, input := range []string{"invalid", "xyz", "123", "-5m"} {
		_, err := ParseRetentionPolicy(input)
		if err == nil {
			t.Errorf("expected error for '%s'", input)
		}
	}
}

// ruleKeys returns the keys of the rules map for error messages.
func ruleKeys(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) []string {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	return keys
}

// formatRuleKeys is a test helper that formats rule keys for error output.
func formatRuleKeys(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) string {
	return fmt.Sprintf("%v", ruleKeys(rules))
}
