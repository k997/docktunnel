package label

import (
	"testing"
)

func TestDecodeDockTunnel_BasicService(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "example.com",
		"docktunnel.web.service":  "http://localhost:8080",
	}

	services := decodeDockTunnel(labels)
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}

	web, ok := services["web"]
	if !ok {
		t.Fatal("expected 'web' service")
	}
	if web.Hostname != "example.com" {
		t.Errorf("expected hostname 'example.com', got '%s'", web.Hostname)
	}
	if web.Service != "http://localhost:8080" {
		t.Errorf("expected service 'http://localhost:8080', got '%s'", web.Service)
	}
}

func TestDecodeDockTunnel_MultipleServices(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "web.example.com",
		"docktunnel.web.service":  "http://localhost:8080",
		"docktunnel.api.hostname": "api.example.com",
		"docktunnel.api.service":  "http://localhost:3000",
		"docktunnel.api.path":     "/v1",
	}

	services := decodeDockTunnel(labels)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}

	api := services["api"]
	if api.Hostname != "api.example.com" {
		t.Errorf("expected api hostname 'api.example.com', got '%s'", api.Hostname)
	}
	if api.Path != "/v1" {
		t.Errorf("expected api path '/v1', got '%s'", api.Path)
	}
}

func TestDecodeDockTunnel_OriginRequest(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":                           "true",
		"docktunnel.web.hostname":                     "example.com",
		"docktunnel.web.originRequest.connectTimeout": "30s",
		"docktunnel.web.originRequest.noTLSVerify":    "true",
		"docktunnel.web.originRequest.proxyAddress":   "127.0.0.1",
		"docktunnel.web.originRequest.proxyPort":      "9050",
		"docktunnel.web.originRequest.matchSNItoHost": "true",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]

	if web.ConnectTimeout != "30s" {
		t.Errorf("expected connectTimeout '30s', got '%s'", web.ConnectTimeout)
	}
	if web.NoTLSVerify != "true" {
		t.Errorf("expected noTLSVerify 'true', got '%s'", web.NoTLSVerify)
	}
	if web.ProxyAddress != "127.0.0.1" {
		t.Errorf("expected proxyAddress '127.0.0.1', got '%s'", web.ProxyAddress)
	}
	if web.ProxyPort != "9050" {
		t.Errorf("expected proxyPort '9050', got '%s'", web.ProxyPort)
	}
	if web.MatchSNItoHost != "true" {
		t.Errorf("expected matchSNItoHost 'true', got '%s'", web.MatchSNItoHost)
	}
}

func TestDecodeDockTunnel_AccessConfig(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":                            "true",
		"docktunnel.api.hostname":                      "api.example.com",
		"docktunnel.api.originRequest.access.required": "true",
		"docktunnel.api.originRequest.access.teamName": "my-team",
		"docktunnel.api.originRequest.access.audTag":   "tag1, tag2",
	}

	services := decodeDockTunnel(labels)
	api := services["api"]

	if api.AccessRequired != "true" {
		t.Errorf("expected access.required 'true', got '%s'", api.AccessRequired)
	}
	if api.AccessTeamName != "my-team" {
		t.Errorf("expected access.teamName 'my-team', got '%s'", api.AccessTeamName)
	}
	if api.AccessAUDTag != "tag1, tag2" {
		t.Errorf("expected access.audTag 'tag1, tag2', got '%s'", api.AccessAUDTag)
	}
}

func TestDecodeDockTunnel_SkipsNonDockTunnelLabels(t *testing.T) {
	labels := map[string]string{
		"traefik.enable":                  "true",
		"traefik.http.routers.myapp.rule": "Host(`example.com`)",
		"com.docker.compose.service":      "myapp",
		"docktunnel.enable":               "true",
		"docktunnel.web.hostname":         "web.example.com",
	}

	services := decodeDockTunnel(labels)
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}
	if _, ok := services["web"]; !ok {
		t.Error("expected 'web' service")
	}
}

func TestDecodeDockTunnel_SkipsDangerousValues(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "example.com; rm -rf /",
		"docktunnel.web.service":  "http://localhost:8080",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web.Hostname != "" {
		t.Errorf("expected empty hostname (rejected), got '%s'", web.Hostname)
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	testCases := []struct {
		name      string
		value     string
		wantValid bool
	}{
		{"valid hostname", "example.com", true},
		{"valid URL", "http://localhost:8080", true},
		{"semicolon", "example.com; rm -rf /", false},
		{"ampersand", "example.com & malicious", false},
		{"pipe", "example.com | cat", false},
		{"backtick", "example.com`whoami`", false},
		{"dollar", "example.com$(cmd)", false},
		{"single quote", "example.com' OR '1'='1", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeLabelValue("test.label", tc.value)
			if result != tc.wantValid {
				t.Errorf("sanitizeLabelValue(%q) = %v, want %v", tc.value, result, tc.wantValid)
			}
		})
	}
}
