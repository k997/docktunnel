package label

import (
	"log/slog"
	"strings"
	"sync"
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

	spec, ok := specs["http:myapp@example.com"]
	if !ok {
		t.Fatalf("expected 'http:myapp@example.com' key, got keys: %v", mapKeys(specs))
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

	if _, ok := specs["http:myapp@a.com"]; !ok {
		t.Error("expected 'http:myapp@a.com'")
	}
	if _, ok := specs["http:myapp@b.com"]; !ok {
		t.Error("expected 'http:myapp@b.com'")
	}
}

// TestDecodeTraefikToSpecs_RejectsBooleanRule verifies that a rule combining
// Host and Path with '&&' is rejected (A2): boolean operators are not allowed,
// so no spec may be derived from it.
func TestDecodeTraefikToSpecs_RejectsBooleanRule(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`) && Path(`/api`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("expected 0 specs for rejected rule, got %d: %v", len(specs), mapKeys(specs))
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

	spec := specs["tcp:ssh@ssh.example.com"]
	if spec == nil {
		t.Fatal("expected spec for 'tcp:ssh@ssh.example.com'")
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
	spec := specs["http:myapp@example.com"]
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

// --- P3-1: HostSNI extraction handles multi-value rules in every quote style
// (backtick / double quote / single quote); single-value behavior is unchanged.

func TestExtractHostSNIFromRule(t *testing.T) {
	tests := []struct {
		name     string
		rule     string
		expected []string
	}{
		{"single backtick", "HostSNI(`ssh.example.com`)", []string{"ssh.example.com"}},
		{"single double-quote", `HostSNI("ssh.example.com")`, []string{"ssh.example.com"}},
		{"single single-quote", "HostSNI('ssh.example.com')", []string{"ssh.example.com"}},
		{"multi backtick", "HostSNI(`a.com`, `b.com`)", []string{"a.com", "b.com"}},
		{"multi double-quote", `HostSNI("a.com","b.com")`, []string{"a.com", "b.com"}},
		{"multi single-quote", "HostSNI('a.com','b.com')", []string{"a.com", "b.com"}},
		{"multi mixed quotes", `HostSNI(` + "`a.com`" + `, "b.com")`, []string{"a.com", "b.com"}},
		{"unquoted yields no hostnames", "HostSNI(a.com)", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHostSNIFromRule(tt.rule)
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

// TestDecodeTraefikToSpecs_MultiValueHostSNI verifies that a multi-value
// HostSNI rule produces one TCP spec per hostname for all three quote styles,
// and that a single-value rule still produces exactly one spec (P3-1).
func TestDecodeTraefikToSpecs_MultiValueHostSNI(t *testing.T) {
	styles := []struct {
		name  string
		rule  string
		hosts []string
	}{
		{"backtick", "HostSNI(`a.example.com`,`b.example.com`)", []string{"a.example.com", "b.example.com"}},
		{"double-quote", `HostSNI("a.example.com","b.example.com")`, []string{"a.example.com", "b.example.com"}},
		{"single-quote", "HostSNI('a.example.com','b.example.com')", []string{"a.example.com", "b.example.com"}},
	}
	for _, style := range styles {
		t.Run(style.name, func(t *testing.T) {
			labels := map[string]string{
				"traefik.tcp.routers.ssh.rule":                      style.rule,
				"traefik.tcp.services.ssh.loadbalancer.server.port": "2222",
			}
			specs := decodeTraefikToSpecs(labels, nil)
			if len(specs) != len(style.hosts) {
				t.Fatalf("expected %d specs, got %d: %v", len(style.hosts), len(specs), mapKeys(specs))
			}
			for _, h := range style.hosts {
				if _, ok := specs["tcp:ssh@"+h]; !ok {
					t.Errorf("expected spec 'tcp:ssh@%s', got keys: %v", h, mapKeys(specs))
				}
			}
		})
	}

	// 单值无回归
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":                      "HostSNI(`ssh.example.com`)",
		"traefik.tcp.services.ssh.loadbalancer.server.port": "2222",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 1 {
		t.Fatalf("single-value HostSNI: expected 1 spec, got %d: %v", len(specs), mapKeys(specs))
	}
	if _, ok := specs["tcp:ssh@ssh.example.com"]; !ok {
		t.Errorf("expected spec 'tcp:ssh@ssh.example.com', got keys: %v", mapKeys(specs))
	}
}

// --- A2: rule validation ---

func TestValidateTraefikRule(t *testing.T) {
	validHTTP := []string{
		"Host(`example.com`)",
		`Host("example.com")`,
		"Host('example.com')",
		"Host(`a.com`, `b.com`)",
		"HOST(`EXAMPLE.COM`)",
		"Path(`/api`)",
	}
	for _, rule := range validHTTP {
		if ok, reason := validateTraefikRule(rule); !ok {
			t.Errorf("validateTraefikRule(%q) should be valid, got reason %q", rule, reason)
		}
	}

	validTCP := []string{
		"HostSNI(`ssh.example.com`)",
		`HostSNI("ssh.example.com")`,
		"HostSNI(`a.com`, `b.com`)",
	}
	for _, rule := range validTCP {
		if ok, reason := validateTraefikRule(rule); !ok {
			t.Errorf("validateTraefikRule(%q) should be valid, got reason %q", rule, reason)
		}
	}

	invalid := []string{
		"",                                 // empty
		"!Host(`example.com`)",             // negation
		"HostRegexp(`.*\\.example\\.com`)", // regexp host
		"Host(`a.com`) && Path(`/api`)",    // &&
		"Host(`a.com`) || Host(`b.com`)",   // ||
		"PathPrefix(`/api`)",               // prefix
		"PathRegexp(`/api`)",               // regexp path
		"Method(`GET`)",                    // method
		"Header(`X-Foo`, `bar`)",           // header
		"Query(`a`, `b`)",                  // query
		"ClientIP(`10.0.0.0/8`)",           // client IP
		"Host(`a.com`) && IP(`1.2.3.4`)",   // IP matcher
		"HostSNI(`x.com`) && Path(`/y`)",   // TCP with extra clause
		"HostSNI(`x.com`) Host(`y.com`)",   // TCP with HTTP clause
		"Host(`x.com`) Extra(`y`)",         // unsupported content outside clauses
	}
	for _, rule := range invalid {
		if ok, _ := validateTraefikRule(rule); ok {
			t.Errorf("validateTraefikRule(%q) should be rejected", rule)
		}
	}
}

func TestDecodeTraefikToSpecs_RejectsUnsafeRules(t *testing.T) {
	unsafeRules := []string{
		"!Host(`internal.example.com`)",
		"HostRegexp(`.*\\.example\\.com`)",
		"Host(`a.com`) && Path(`/api`)",
		"Host(`a.com`) || Host(`b.com`)",
		"PathPrefix(`/api`)",
		"Method(`GET`)",
	}

	for _, rule := range unsafeRules {
		labels := map[string]string{
			"traefik.http.routers.bad.rule":                      rule,
			"traefik.http.services.bad.loadbalancer.server.port": "8080",
		}
		specs := decodeTraefikToSpecs(labels, nil)
		if len(specs) != 0 {
			t.Errorf("rule %q should produce no specs, got %v", rule, mapKeys(specs))
		}
	}
}

// TestDecodeTraefikToSpecs_NegatedHostNotInverted is the P0 regression: a
// !Host(x) rule must never be inverted into a "route x" rule.
func TestDecodeTraefikToSpecs_NegatedHostNotInverted(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.internal.rule":                      "!Host(`internal.example.com`)",
		"traefik.http.services.internal.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("!Host rule must be rejected, not inverted; got specs: %v", mapKeys(specs))
	}
	if _, ok := specs["http:internal@internal.example.com"]; ok {
		t.Fatal("negated host must not be routed")
	}
}

func TestDecodeTraefikToSpecs_TCPRuleWithoutHostSNIRejected(t *testing.T) {
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":                      "Host(`ssh.example.com`)",
		"traefik.tcp.services.ssh.loadbalancer.server.port": "2222",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("TCP router without HostSNI must produce no specs, got %v", mapKeys(specs))
	}
}

// --- A3: case normalization ---

func TestDecodeTraefikToSpecs_NormalizesHostnameCase(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "HOST(`EXAMPLE.COM`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatalf("expected lower-case key 'http:myapp@example.com', got keys: %v", mapKeys(specs))
	}
	if spec.Hostname != "example.com" {
		t.Errorf("expected hostname 'example.com', got '%s'", spec.Hostname)
	}
}

func TestExtractHostsFromRule_CaseInsensitive(t *testing.T) {
	result := extractHostsFromRule("hOsT(`a.com`)")
	if len(result) != 1 || result[0] != "a.com" {
		t.Errorf("expected [a.com], got %v", result)
	}
}

func TestExtractPathsFromRule_CaseInsensitive(t *testing.T) {
	result := extractPathsFromRule("pAtH(`/api`)")
	if len(result) != 1 || result[0] != "/api" {
		t.Errorf("expected [/api], got %v", result)
	}
}

// --- A4: port parse failures must not panic ---

func TestDecodeTraefikToSpecs_InvalidHTTPPortNoPanic(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "not-a-port",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec (invalid port must not drop the router)")
	}
	if spec.Port != 0 {
		t.Errorf("expected port 0 on parse failure, got %d", spec.Port)
	}
}

func TestDecodeTraefikToSpecs_InvalidTCPPortNoPanic(t *testing.T) {
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":                      "HostSNI(`ssh.example.com`)",
		"traefik.tcp.services.ssh.loadbalancer.server.port": "oops",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["tcp:ssh@ssh.example.com"]
	if spec == nil {
		t.Fatal("expected spec (invalid port must not drop the router)")
	}
	if spec.Port != 0 {
		t.Errorf("expected port 0 on parse failure, got %d", spec.Port)
	}
}

// --- A5: server.url priority ---

func TestDecodeTraefikToSpecs_ServerURLPriority(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                        "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.url":    "https://172.17.0.2:9443",
		"traefik.http.services.myapp.loadbalancer.server.port":   "8080",
		"traefik.http.services.myapp.loadbalancer.server.scheme": "http",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec")
	}
	if spec.Port != 9443 {
		t.Errorf("server.url port should win, expected 9443, got %d", spec.Port)
	}
	if spec.Scheme != "https" {
		t.Errorf("server.url scheme should win, expected 'https', got '%s'", spec.Scheme)
	}
}

// --- A6: a single bad label must not destroy the whole tree ---

func TestDecodeTraefikToSpecs_InvalidLabelDoesNotBreakTree(t *testing.T) {
	labels := map[string]string{
		// Uppercase root: previously killed the entire tree via the
		// EqualFold filter + case-sensitive root check mismatch.
		"TRAEFIK.http.routers.bad.rule":                     "Host(`bad.example.com`)",
		"traefik.http..broken.rule":                         "Host(`broken.example.com`)",
		"traefik.http.routers.ok.rule":                      "Host(`ok.example.com`)",
		"traefik.http.services.ok.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec (bad labels dropped), got %d: %v", len(specs), mapKeys(specs))
	}
	spec := specs["http:ok@ok.example.com"]
	if spec == nil {
		t.Fatalf("expected 'http:ok@ok.example.com', got keys: %v", mapKeys(specs))
	}
	if spec.Port != 8080 {
		t.Errorf("expected port 8080, got %d", spec.Port)
	}
}

// --- A7: populateLoadBalancer skips servers without fields ---

func TestPopulateLoadBalancer_SkipsEmptyServer(t *testing.T) {
	node := &Node{
		Name: "loadbalancer",
		Children: []*Node{
			{Name: "server", Children: []*Node{}},
			{Name: "server", Children: []*Node{{Name: "port", Value: "8080"}}},
		},
	}
	lb := &ServersLoadBalancer{}
	populateLoadBalancer(node, lb)

	if len(lb.Servers) != 1 {
		t.Fatalf("expected 1 server (empty one skipped), got %d", len(lb.Servers))
	}
	if lb.Servers[0].Port != "8080" {
		t.Errorf("expected port '8080', got %q", lb.Servers[0].Port)
	}
}

// --- A8: HTTP and TCP routers with the same name must not collide ---

func TestDecodeTraefikToSpecs_HTTPAndTCPSameName(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.app.rule":                      "Host(`app.example.com`)",
		"traefik.http.services.app.loadbalancer.server.port": "8080",
		"traefik.tcp.routers.app.rule":                       "HostSNI(`app.example.com`)",
		"traefik.tcp.services.app.loadbalancer.server.port":  "2222",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs (http + tcp), got %d: %v", len(specs), mapKeys(specs))
	}
	httpSpec := specs["http:app@app.example.com"]
	if httpSpec == nil || httpSpec.Port != 8080 || httpSpec.Scheme != "http" {
		t.Errorf("expected http spec with port 8080, got %+v", httpSpec)
	}
	tcpSpec := specs["tcp:app@app.example.com"]
	if tcpSpec == nil || tcpSpec.Port != 2222 || tcpSpec.Scheme != "tcp" {
		t.Errorf("expected tcp spec with port 2222, got %+v", tcpSpec)
	}
}

// --- A9: traefik hostnames go through the same hostname validation ---

func TestDecodeTraefikToSpecs_RejectsInvalidHostname(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.bad.rule":                      "Host(`example.com; rm -rf /`)",
		"traefik.http.services.bad.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("expected 0 specs for invalid hostname, got %v", mapKeys(specs))
	}
}

// --- A11: traefik.docker.network selects the container network ---

func TestDecodeTraefikToSpecs_NetworkLabel(t *testing.T) {
	labels := map[string]string{
		"traefik.docker.network":                               "custom",
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}

	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom": {IPAddress: "10.0.0.5"},
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	specs := decodeTraefikToSpecs(labels, containerInfo)
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec")
	}
	if spec.Network != "custom" {
		t.Errorf("expected Network 'custom', got '%s'", spec.Network)
	}
	if spec.ServiceURL != "http://10.0.0.5:8080" {
		t.Errorf("expected ServiceURL 'http://10.0.0.5:8080' from custom network, got '%s'", spec.ServiceURL)
	}
}

func TestDecodeTraefikToSpecs_UnknownNetworkFallsBack(t *testing.T) {
	labels := map[string]string{
		"traefik.docker.network":                               "missing-net",
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
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
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatal("expected spec")
	}
	if spec.ServiceURL != "http://172.17.0.2:8080" {
		t.Errorf("expected fallback ServiceURL 'http://172.17.0.2:8080', got '%s'", spec.ServiceURL)
	}
}

// --- A13: traefik-derived specs must not carry an empty OriginRequest ---

func TestDecodeTraefikToSpecs_OriginRequestNil(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host(`example.com`)",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
		"traefik.tcp.routers.ssh.rule":                         "HostSNI(`ssh.example.com`)",
		"traefik.tcp.services.ssh.loadbalancer.server.port":    "2222",
	}

	specs := decodeTraefikToSpecs(labels, nil)
	httpSpec := specs["http:myapp@example.com"]
	if httpSpec == nil {
		t.Fatal("expected http spec")
	}
	if httpSpec.OriginRequest != nil {
		t.Error("http spec OriginRequest should be nil when no originRequest labels are present")
	}
	tcpSpec := specs["tcp:ssh@ssh.example.com"]
	if tcpSpec == nil {
		t.Fatal("expected tcp spec")
	}
	if tcpSpec.OriginRequest != nil {
		t.Error("tcp spec OriginRequest should be nil when no originRequest labels are present")
	}
}

func mapKeys[M ~map[string]V, V any](m M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Review R2: a router referencing middlewares must never be exposed without
// them — that would publish an unauthenticated copy of an auth-protected
// route.
func TestDecodeTraefikToSpecs_RejectsRouterWithMiddlewares(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.admin.rule":                      "Host(`admin.example.com`)",
		"traefik.http.routers.admin.middlewares":               "auth@file",
		"traefik.http.services.admin.loadbalancer.server.port": "8080",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("router with middlewares must be rejected, got %d specs", len(specs))
	}
}

// Review R3: a Path-only router (no Host clause) must be skipped loudly
// instead of silently dropped.
func TestDecodeTraefikToSpecs_PathOnlyRouterProducesNoSpecs(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.api.rule":                      "Path(`/api`)",
		"traefik.http.services.api.loadbalancer.server.port": "8080",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("Path-only router must produce no specs, got %d", len(specs))
	}
}

// Review R2 regression: legitimate hostnames or paths containing words like
// "query" or "method" must not be mistaken for unsupported matcher
// invocations (matchers are only matched as name followed by "(").
func TestValidateTraefikRule_AllowsWordsInsideClauses(t *testing.T) {
	for _, rule := range []string{
		"Host(`myquery.example.com`)",
		"Host(`myhostregexp.example.com`)",
		"Host(`api.example.com`) Path(`/query`)",
		"Host(`api.example.com`) Path(`/method`)",
		"Host(`api.example.com`) Path(`/pathprefix`)",
	} {
		if ok, reason := validateTraefikRule(rule); !ok {
			t.Errorf("rule %q rejected: %s", rule, reason)
		}
	}
}

func TestValidateTraefikRule_RejectsUnsafeClauses(t *testing.T) {
	for _, rule := range []string{
		"!Host(`internal.com`)",
		"Host(`a.com`) || Host(`b.com`)",
		"Host(`a.com`) && Host(`b.com`)",
		"HostRegexp(`{sub}.x.com`)",
		"Host(`x.com`) && PathPrefix(`/api`)",
		"Host(`x.com`) && Method(`GET`)",
		"Host(`x.com`) && Header(`X-Token`, `s`)",
	} {
		if ok, _ := validateTraefikRule(rule); ok {
			t.Errorf("rule %q should have been rejected", rule)
		}
	}
}

// --- F1: trailing-']' keys must be dropped up front, never reach the
// DecodeToNode slice that panics on a ']' without '[' ---

func TestValidTraefikLabelKey(t *testing.T) {
	valid := []string{
		"traefik.http.routers.app.rule",
		"traefik.http.services.app.loadbalancer.server.port",
		"traefik.tcp.routers.ssh.rule",
		"traefik.docker.network",
	}
	for _, k := range valid {
		if !validTraefikLabelKey(k) {
			t.Errorf("expected valid key: %q", k)
		}
	}

	invalid := []string{
		"TRAEFIK.http.routers.app.rule", // root casing
		"traefik.http..broken.rule",     // empty segment
		"traefik.http.routers.[0].rule", // segment starts with '['
		"traefik.http.routers.foo]",     // F1: ']' suffix without '['
		"traefik.http.routers.a]b",      // F1: ']' suffix without '['
	}
	for _, k := range invalid {
		if validTraefikLabelKey(k) {
			t.Errorf("expected invalid key: %q", k)
		}
	}
}

func TestDecodeTraefikLabels_TrailingBracketKeysNoPanic(t *testing.T) {
	labels := map[string]string{
		// F1: these keys used to panic in DecodeToNode (strings.Index returns
		// -1 for a ']' without '[', slicing v[:indexLeft] out of range).
		"traefik.http.routers.foo]":                         "Host(`evil.example.com`)",
		"traefik.http.routers.a]b":                          "Host(`evil2.example.com`)",
		"traefik.http.routers.ok.rule":                      "Host(`ok.example.com`)",
		"traefik.http.services.ok.loadbalancer.server.port": "8080",
	}

	conf := decodeTraefikLabels(labels) // must not panic
	if conf == nil {
		t.Fatal("expected non-nil configuration from the remaining valid labels")
	}
	if _, ok := conf.HTTP.Routers["foo]"]; ok {
		t.Error("malformed key 'traefik.http.routers.foo]' must be dropped")
	}
	if _, ok := conf.HTTP.Routers["a]b"]; ok {
		t.Error("malformed key 'traefik.http.routers.a]b' must be dropped")
	}
	if _, ok := conf.HTTP.Routers["ok"]; !ok {
		t.Error("valid router 'ok' must still be parsed")
	}

	specs := decodeTraefikToSpecs(labels, nil) // must not panic either
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec from valid labels, got %d: %v", len(specs), mapKeys(specs))
	}
	if _, ok := specs["http:ok@ok.example.com"]; !ok {
		t.Fatalf("expected 'http:ok@ok.example.com', got keys: %v", mapKeys(specs))
	}
	for _, key := range []string{"http:foo@evil.example.com", "http:a]b@evil2.example.com"} {
		if _, ok := specs[key]; ok {
			t.Errorf("malicious label must not produce a spec, got key %q", key)
		}
	}
}

// --- F2: single-quoted Path() and HostSNI() clauses must be extracted ---

func TestExtractPathsFromRule_SingleQuoted(t *testing.T) {
	result := extractPathsFromRule("Host('example.com') Path('/api')")
	if len(result) != 1 || result[0] != "/api" {
		t.Errorf("expected [/api], got %v", result)
	}
}

func TestExtractHostSNIFromRule_SingleQuoted(t *testing.T) {
	result := extractHostSNIFromRule("HostSNI('ssh.example.com')")
	if len(result) != 1 || result[0] != "ssh.example.com" {
		t.Errorf("expected [ssh.example.com], got %v", result)
	}
}

func TestDecodeTraefikToSpecs_SingleQuotedPath(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                      "Host('example.com') Path('/api')",
		"traefik.http.services.myapp.loadbalancer.server.port": "8080",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["http:myapp@example.com"]
	if spec == nil {
		t.Fatalf("expected spec, got keys: %v", mapKeys(specs))
	}
	if spec.Path != "/api" {
		t.Errorf("single-quoted Path must be extracted, expected '/api', got '%s'", spec.Path)
	}
}

func TestDecodeTraefikToSpecs_SingleQuotedHostSNI(t *testing.T) {
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":                      "HostSNI('ssh.example.com')",
		"traefik.tcp.services.ssh.loadbalancer.server.port": "2222",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["tcp:ssh@ssh.example.com"]
	if spec == nil {
		t.Fatalf("expected spec, got keys: %v", mapKeys(specs))
	}
	if spec.Port != 2222 {
		t.Errorf("single-quoted HostSNI must be extracted, expected port 2222, got %d", spec.Port)
	}
}

// --- F4: a router whose service has no usable server must be skipped, never
// silently routed to a port-0 address ---

func TestDecodeTraefikToSpecs_WeightedServiceNoSpec(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.app.rule":                           "Host(`example.com`)",
		"traefik.http.routers.app.service":                        "app",
		"traefik.http.services.app.weighted.services.svc1.weight": "1",
		"traefik.http.services.app.weighted.services.svc2.weight": "2",
		"traefik.http.services.svc1.loadbalancer.server.port":     "8080",
		"traefik.http.services.svc2.loadbalancer.server.port":     "9090",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("router pointing at a weighted service must produce no specs (no silent port-0 route), got %v", mapKeys(specs))
	}
}

func TestDecodeTraefikToSpecs_ServiceWithoutServerNoSpec(t *testing.T) {
	// Router explicitly references a service that exists but has no server.
	labels := map[string]string{
		"traefik.http.routers.app.rule":    "Host(`example.com`)",
		"traefik.http.routers.app.service": "missing-svc",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("router with missing service must produce no specs, got %v", mapKeys(specs))
	}
}

func TestDecodeTraefikToSpecs_NormalLoadBalancerUnaffected(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.app.rule":                      "Host(`example.com`)",
		"traefik.http.services.app.loadbalancer.server.port": "8080",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	spec := specs["http:app@example.com"]
	if spec == nil {
		t.Fatalf("normal loadbalancer.server service must still produce a spec, got keys: %v", mapKeys(specs))
	}
	if spec.Port != 8080 {
		t.Errorf("expected port 8080, got %d", spec.Port)
	}
}

func TestDecodeTraefikToSpecs_TCPRouterMissingServiceNoSpec(t *testing.T) {
	labels := map[string]string{
		"traefik.tcp.routers.ssh.rule":    "HostSNI(`ssh.example.com`)",
		"traefik.tcp.routers.ssh.service": "missing-svc",
	}
	specs := decodeTraefikToSpecs(labels, nil)
	if len(specs) != 0 {
		t.Fatalf("TCP router with missing service must produce no specs, got %v", mapKeys(specs))
	}
}

// --- P3-2: traefik.udp.* labels must be dropped with an Info log, never
// silently, and must not panic or leak into the parse result ---

func TestDecodeTraefikToSpecs_UDPLabelsIgnored(t *testing.T) {
	lines := captureLabelSlog(t)
	labels := map[string]string{
		"traefik.udp.routers.quic.rule":                      "HostSNI(`quic.example.com`)",
		"traefik.udp.services.quic.loadbalancer.server.port": "443",
		"traefik.http.routers.app.rule":                      "Host(`app.example.com`)",
		"traefik.http.services.app.loadbalancer.server.port": "8080",
	}

	specs := decodeTraefikToSpecs(labels, nil) // must not panic
	if len(specs) != 1 {
		t.Fatalf("expected only the HTTP spec (UDP labels ignored), got %d: %v", len(specs), mapKeys(specs))
	}
	if _, ok := specs["http:app@app.example.com"]; !ok {
		t.Errorf("expected 'http:app@app.example.com', got keys: %v", mapKeys(specs))
	}
	if _, ok := specs["tcp:quic@quic.example.com"]; ok {
		t.Error("UDP labels must not produce a spec")
	}

	// 每个 udp 标签应各打一条 Info 日志（大小写不敏感前缀）
	var sawUDPInfo int
	for _, line := range lines.snapshot() {
		if strings.Contains(line, "Traefik UDP labels are not supported, ignoring") {
			sawUDPInfo++
		}
	}
	if sawUDPInfo != 2 {
		t.Errorf("expected 2 Info logs (one per UDP label), got %d, lines: %v", sawUDPInfo, lines.snapshot())
	}
}

// captureLabelSlog swaps slog.Default() with a handler that records every
// emitted line, and restores the original handler on test cleanup.
func captureLabelSlog(t *testing.T) *labelSlogCapture {
	t.Helper()
	orig := slog.Default()
	c := &labelSlogCapture{}
	slog.SetDefault(slog.New(slog.NewTextHandler(c, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return c
}

type labelSlogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *labelSlogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}

func (c *labelSlogCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}
