package label

import (
	"testing"
)

func TestDecodeToNode_SingleLabel(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule": "Host(`example.com`)",
	}

	node, err := DecodeToNode(labels, "traefik", "traefik.http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if node.Name != "traefik" {
		t.Errorf("expected root name 'traefik', got '%s'", node.Name)
	}

	if len(node.Children) == 0 {
		t.Fatal("expected children on root node")
	}
}

func TestDecodeToNode_FilterSkipsNonMatching(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule": "Host(`example.com`)",
		"traefik.tcp.routers.myapp.rule":  "HostSNI(`ssh.com`)",
		"com.docker.compose.service":      "myapp",
	}

	node, err := DecodeToNode(labels, "traefik", "traefik.http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if node == nil {
		t.Fatal("expected non-nil node")
	}

	foundHTTP := false
	foundTCP := false
	for _, child := range node.Children {
		if child.Name == "http" {
			foundHTTP = true
		}
		if child.Name == "tcp" {
			foundTCP = true
		}
	}

	if !foundHTTP {
		t.Error("expected http child node")
	}
	if foundTCP {
		t.Error("tcp should be filtered out when only traefik.http filter is used")
	}
}

func TestDecodeToNode_InvalidRoot(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule": "Host(`example.com`)",
		"other.label":                     "value",
	}

	_, err := DecodeToNode(labels, "app")
	if err == nil {
		t.Error("expected error for invalid root")
	}
}

func TestDecodeToNode_NoFilters(t *testing.T) {
	labels := map[string]string{
		"app.host": "example.com",
		"app.port": "8080",
	}

	node, err := DecodeToNode(labels, "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(node.Children) != 2 {
		t.Errorf("expected 2 children, got %d", len(node.Children))
	}
}

func TestDecodeToNode_NestedPath(t *testing.T) {
	labels := map[string]string{
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	node, err := DecodeToNode(labels, "traefik", "traefik.http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	current := node
	path := []string{"http", "services", "myapp", "loadbalancer", "server", "port"}
	for _, name := range path {
		found := false
		for _, child := range current.Children {
			if child.Name == name {
				current = child
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected child '%s' not found in path", name)
		}
	}

	if current.Value != "8080" {
		t.Errorf("expected leaf value '8080', got '%s'", current.Value)
	}
}
