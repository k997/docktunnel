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

func TestValidHostname(t *testing.T) {
	testCases := []struct {
		name      string
		value     string
		wantValid bool
	}{
		{"valid hostname", "example.com", true},
		{"wildcard", "*.example.com", true},
		{"dashes and underscores", "my_app-1.example.com", true},
		{"semicolon", "example.com; rm -rf /", false},
		{"ampersand", "example.com & malicious", false},
		{"pipe", "example.com | cat", false},
		{"backtick", "example.com`whoami`", false},
		{"dollar parens", "example.com$(cmd)", false},
		{"single quote", "example.com' OR '1'='1", false},
		{"whitespace", "example .com", false},
		{"slash", "example.com/foo", false},
		{"empty", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validHostname(tc.value); got != tc.wantValid {
				t.Errorf("validHostname(%q) = %v, want %v", tc.value, got, tc.wantValid)
			}
		})
	}
}

func TestValidServiceURL(t *testing.T) {
	testCases := []struct {
		name      string
		value     string
		wantValid bool
	}{
		{"http url", "http://localhost:8080", true},
		{"https url", "https://example.com", true},
		{"tcp url", "tcp://10.0.0.1:2222", true},
		{"ftp scheme", "ftp://example.com", false},
		{"no scheme", "localhost:8080", false},
		{"relative path", "/api", false},
		{"empty host", "http://", false},
		{"injection in host", "http://localhost:8080&evil", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validServiceURL(tc.value); got != tc.wantValid {
				t.Errorf("validServiceURL(%q) = %v, want %v", tc.value, got, tc.wantValid)
			}
		})
	}
}

func TestValidPathValue(t *testing.T) {
	testCases := []struct {
		name      string
		value     string
		wantValid bool
	}{
		{"normal path", "/api/v1", true},
		{"newline", "/api\n/v1", false},
		{"carriage return", "/api\r/v1", false},
		{"null byte", "/api\x00/v1", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPathValue(tc.value); got != tc.wantValid {
				t.Errorf("validPathValue(%q) = %v, want %v", tc.value, got, tc.wantValid)
			}
		})
	}
}

// TestSetServiceConfigField_UnknownOriginRequestKey verifies that unknown
// originRequest.* subkeys do not panic or get silently swallowed into a
// real field. We can't easily capture slog output, but we can assert the
// decoder simply doesn't populate anything.
func TestSetServiceConfigField_UnknownOriginRequestKey(t *testing.T) {
	sc := &ServiceConfig{}
	// Walk a few attributes that look plausible but are not in our schema.
	for _, attr := range []string{
		"originRequest.fallbackDelay",
		"originRequest.bogusField",
		"originRequest.access.tls.auth",
	} {
		setServiceConfigField(sc, attr, "some-value")
	}

	// None of these should have populated any known field on ServiceConfig.
	if sc.ConnectTimeout != "" || sc.NoTLSVerify != "" || sc.AccessRequired != "" {
		t.Errorf("unknown originRequest.* keys should not populate any field, got %+v", sc)
	}
}

// TestDecodeDockTunnel_OriginRequestSubkeyIsolated ensures a recognized
// originRequest key still works alongside unknown ones (decoder is
// per-attribute, not all-or-nothing).
func TestDecodeDockTunnel_OriginRequestSubkeyIsolated(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":                           "true",
		"docktunnel.web.hostname":                     "example.com",
		"docktunnel.web.originRequest.connectTimeout": "30s",
		"docktunnel.web.originRequest.fallbackDelay":  "300ms",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web.ConnectTimeout != "30s" {
		t.Errorf("recognized key should still populate: got connectTimeout=%q", web.ConnectTimeout)
	}
}

// --- A1: the Traefik opt-in flag must not become a service ---

func TestDecodeDockTunnel_SkipsTraefikEnableFlag(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":         "true",
		"docktunnel.traefik.enable": "true",
		"docktunnel.web.hostname":   "example.com",
		"docktunnel.web.service":    "http://localhost:8080",
	}

	services := decodeDockTunnel(labels)
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d (keys: %v)", len(services), serviceKeys(services))
	}
	if _, ok := services["traefik"]; ok {
		t.Error("docktunnel.traefik.enable must not be decoded as a service named 'traefik'")
	}
	if _, ok := services["web"]; !ok {
		t.Error("expected 'web' service")
	}
}

// --- A9: per-field validation skips invalid values ---

func TestDecodeDockTunnel_RejectsInvalidHostnameValue(t *testing.T) {
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
	if web.Service != "http://localhost:8080" {
		t.Errorf("valid service should still be set, got '%s'", web.Service)
	}
}

func TestDecodeDockTunnel_RejectsInvalidServiceValue(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "example.com",
		"docktunnel.web.service":  "ftp://example.com",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web.Service != "" {
		t.Errorf("expected empty service (rejected), got '%s'", web.Service)
	}
	if web.Hostname != "example.com" {
		t.Errorf("valid hostname should still be set, got '%s'", web.Hostname)
	}
}

func TestDecodeDockTunnel_RejectsInvalidPathValue(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":       "true",
		"docktunnel.web.hostname": "example.com",
		"docktunnel.web.path":     "/api\n/v2",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web.Path != "" {
		t.Errorf("expected empty path (rejected), got %q", web.Path)
	}
}

// --- A10: aliases and new attributes ---

func TestDecodeDockTunnel_Aliases(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable": "true",

		// top-level access aliases
		"docktunnel.web1.hostname":         "one.example.com",
		"docktunnel.web1.access.required":  "true",
		"docktunnel.web1.access.team_name": "team-underscore",
		"docktunnel.web1.access.aud_tag":   "tag-u1, tag-u2",

		// originRequest access aliases
		"docktunnel.web2.hostname":                       "two.example.com",
		"docktunnel.web2.originRequest.access.team_name": "team-orig",
		"docktunnel.web2.originRequest.access.aud_tag":   "tag-o1, tag-o2",

		// delete_retention / network
		"docktunnel.web3.hostname":         "three.example.com",
		"docktunnel.web3.delete_retention": "7d",
		"docktunnel.web3.network":          "custom",
	}

	services := decodeDockTunnel(labels)

	web1 := services["web1"]
	if web1 == nil {
		t.Fatal("expected 'web1' service")
	}
	if web1.AccessRequired != "true" {
		t.Errorf("expected access.required 'true', got '%s'", web1.AccessRequired)
	}
	if web1.AccessTeamName != "team-underscore" {
		t.Errorf("expected access.team_name 'team-underscore', got '%s'", web1.AccessTeamName)
	}
	if web1.AccessAUDTag != "tag-u1, tag-u2" {
		t.Errorf("expected access.aud_tag 'tag-u1, tag-u2', got '%s'", web1.AccessAUDTag)
	}

	web2 := services["web2"]
	if web2 == nil {
		t.Fatal("expected 'web2' service")
	}
	if web2.AccessTeamName != "team-orig" {
		t.Errorf("expected originRequest.access.team_name 'team-orig', got '%s'", web2.AccessTeamName)
	}
	if web2.AccessAUDTag != "tag-o1, tag-o2" {
		t.Errorf("expected originRequest.access.aud_tag 'tag-o1, tag-o2', got '%s'", web2.AccessAUDTag)
	}

	web3 := services["web3"]
	if web3 == nil {
		t.Fatal("expected 'web3' service")
	}
	if web3.Retention != "7d" {
		t.Errorf("expected delete_retention '7d', got '%s'", web3.Retention)
	}
	if web3.Network != "custom" {
		t.Errorf("expected network 'custom', got '%s'", web3.Network)
	}
}

// --- F3: matchSNItoHost has no Cloudflare v5 SDK field; the label must be
// ignored (with a warning) instead of silently stored ---

func TestDecodeDockTunnel_MatchSNItoHostIgnored(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":                           "true",
		"docktunnel.web.hostname":                     "example.com",
		"docktunnel.web.originRequest.matchSNItoHost": "true",
		"docktunnel.web.matchSniToHost":               "true",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web == nil {
		t.Fatal("expected 'web' service")
	}
	if web.MatchSNItoHost != "" {
		t.Errorf("matchSNItoHost must be ignored, expected empty MatchSNItoHost, got %q", web.MatchSNItoHost)
	}
	if web.Hostname != "example.com" {
		t.Errorf("valid labels must still be decoded, expected hostname 'example.com', got '%s'", web.Hostname)
	}
}

func TestDecodeDockTunnel_AccessAliases(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":              "true",
		"docktunnel.web.hostname":        "example.com",
		"docktunnel.web.access.teamName": "team-camel",
		"docktunnel.web.access.audTag":   "tag-c1, tag-c2",
	}

	services := decodeDockTunnel(labels)
	web := services["web"]
	if web.AccessTeamName != "team-camel" {
		t.Errorf("expected access.teamName 'team-camel', got '%s'", web.AccessTeamName)
	}
	if web.AccessAUDTag != "tag-c1, tag-c2" {
		t.Errorf("expected access.audTag 'tag-c1, tag-c2', got '%s'", web.AccessAUDTag)
	}
}

// --- A12: reserved "enable" namespace ---

func TestDecodeDockTunnel_SkipsReservedEnableNamespace(t *testing.T) {
	labels := map[string]string{
		"docktunnel.enable":          "true",
		"docktunnel.enable.hostname": "should-not-exist.example.com",
		"docktunnel.web.hostname":    "example.com",
	}

	services := decodeDockTunnel(labels)
	if _, ok := services["enable"]; ok {
		t.Error("docktunnel.enable.* labels must be skipped, not decoded as a service")
	}
	if _, ok := services["web"]; !ok {
		t.Error("expected 'web' service")
	}
}

func serviceKeys(services map[string]*ServiceConfig) []string {
	keys := make([]string, 0, len(services))
	for k := range services {
		keys = append(keys, k)
	}
	return keys
}
