package label

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestDecodeTraefikToSpecs_HTTPRouterWithService(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                            "Host(`example.com`)",
		"traefik.http.routers.myapp.service":                         "myapp-svc",
		"traefik.http.services.myapp-svc.loadbalancer.server.port":   "8080",
		"traefik.http.services.myapp-svc.loadbalancer.server.scheme": "https",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	specs := decodeTraefikToSpecs(labels, containerInfo)
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}

	spec, ok := specs["myapp@example.com"]
	if !ok {
		t.Fatalf("expected 'myapp@example.com' key, got keys: %v", mapKeys(specs))
	}
	if spec.Hostname != "example.com" {
		t.Errorf("expected hostname 'example.com', got '%s'", spec.Hostname)
	}
	if spec.Port != 8080 {
		t.Errorf("expected port 8080, got %d", spec.Port)
	}
	if spec.Scheme != "https" {
		t.Errorf("expected scheme 'https', got '%s'", spec.Scheme)
	}
}

func TestDecodeTraefikToSpecs_MultipleHosts(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`a.com`, `b.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}

	if _, ok := specs["myapp@a.com"]; !ok {
		t.Error("expected 'myapp@a.com'")
	}
	if _, ok := specs["myapp@b.com"]; !ok {
		t.Error("expected 'myapp@b.com'")
	}
}

func TestDecodeTraefikToSpecs_HostAndPath(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`) && Path(`/api`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec for 'myapp@example.com'")
	}
	if spec.Path != "/api" {
		t.Errorf("expected path '/api', got '%s'", spec.Path)
	}
}

func TestDecodeTraefikToSpecs_TCPRouter(t *testing.T) {
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":                          "HostSNI(`ssh.example.com`)",
		"traefik.tcp.routers.ssh.service":                       "ssh-svc",
		"traefik.tcp.services.ssh-svc.loadbalancer.server.port": "2222",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}

	spec := specs["ssh@ssh.example.com"]
	if spec == nil {
		t.Fatal("expected spec for 'ssh@ssh.example.com'")
	}
	if spec.Scheme != "tcp" {
		t.Errorf("expected scheme 'tcp', got '%s'", spec.Scheme)
	}
	if spec.Port != 2222 {
		t.Errorf("expected port 2222, got %d", spec.Port)
	}
}

func TestDecodeTraefikToSpecs_NoTraefikLabels(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "example.com",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}
}

func TestDecodeTraefikToSpecs_DefaultServiceName(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "3000",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec")
	}
	if spec.Port != 3000 {
		t.Errorf("expected port 3000, got %d", spec.Port)
	}
}

func TestExtractHostsFromRule(t *testing.T) {
	tests := []struct {
		name     string
		rule     string
		expected []string
	}{
		{"single host backtick", "Host(`example.com`)", []string{"example.com"}},
		{"single host double-quote", `Host("example.com")`, []string{"example.com"}},
		{"single host single-quote", "Host('example.com')", []string{"example.com"}},
		{"multiple hosts backtick", "Host(`a.com`, `b.com`)", []string{"a.com", "b.com"}},
		{"host and path", "Host(`example.com`) && Path(`/api`)", []string{"example.com"}},
		{"host and path double-quote", `Host("example.com") && Path("/api")`, []string{"example.com"}},
		{"no host", "Path(`/api`)", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHostsFromRule(tt.rule)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
				return
			}
			for i, h := range result {
				if h != tt.expected[i] {
					t.Errorf("expected[%d] %s, got %s", i, tt.expected[i], h)
				}
			}
		})
	}
}

func TestExtractHostSNIFromRule(t *testing.T) {
	result := extractHostSNIFromRule("HostSNI(`ssh.example.com`)")
	if len(result) != 1 || result[0] != "ssh.example.com" {
		t.Errorf("expected [ssh.example.com], got %v", result)
	}
}

func mapKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
