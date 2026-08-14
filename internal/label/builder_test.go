package label

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestGetContainerIP_Bridge(t *testing.T) {
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

	ip := GetContainerIP(containerInfo)
	if ip != "172.17.0.2" {
		t.Errorf("expected '172.17.0.2', got '%s'", ip)
	}
}

func TestGetContainerIP_HostMode(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "host"},
		},
		NetworkSettings: &container.NetworkSettings{},
	}

	ip := GetContainerIP(containerInfo)
	if ip != "localhost" {
		t.Errorf("expected 'localhost', got '%s'", ip)
	}
}

func TestGetContainerIP_NilSettings(t *testing.T) {
	containerInfo := &container.InspectResponse{}
	ip := GetContainerIP(containerInfo)
	if ip != "" {
		t.Errorf("expected empty, got '%s'", ip)
	}
}

func TestGetContainerIP_NoIP(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: ""},
			},
		},
	}

	ip := GetContainerIP(containerInfo)
	if ip != "" {
		t.Errorf("expected empty, got '%s'", ip)
	}
}

// TestGetContainerIP_MultiNetworkDeterministic verifies the fallback loop
// picks the same IP regardless of Go's randomized map iteration order.
func TestGetContainerIP_MultiNetworkDeterministic(t *testing.T) {
	build := func() *container.InspectResponse {
		return &container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{
				HostConfig: &container.HostConfig{NetworkMode: "default"},
			},
			NetworkSettings: &container.NetworkSettings{
				Networks: map[string]*network.EndpointSettings{
					"backend":  {IPAddress: "10.0.0.10"},
					"frontend": {IPAddress: "10.0.0.20"},
					"metrics":  {IPAddress: "10.0.0.30"},
				},
			},
		}
	}

	// Rebuild the map several times; map iteration order is randomized, so
	// without sorting we'd see different IPs across runs. With sorting, the
	// "backend" network (alphabetically first) must always win.
	var seen = map[string]int{}
	for range 50 {
		seen[GetContainerIP(build())]++
	}

	if len(seen) != 1 {
		t.Fatalf("expected deterministic single IP, saw distribution: %v", seen)
	}
	if _, ok := seen["10.0.0.10"]; !ok {
		t.Errorf("expected IP from 'backend' (alphabetically first), got %v", seen)
	}
}

// --- A11: GetContainerIPOnNetwork ---

func TestGetContainerIPOnNetwork_EmptyMatchesDefault(t *testing.T) {
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

	if got := GetContainerIPOnNetwork(containerInfo, ""); got != GetContainerIP(containerInfo) {
		t.Errorf("GetContainerIPOnNetwork(\"\") should equal GetContainerIP, got %q", got)
	}
	if got := GetContainerIPOnNetwork(containerInfo, ""); got != "172.17.0.2" {
		t.Errorf("expected '172.17.0.2', got '%s'", got)
	}
}

func TestGetContainerIPOnNetwork_Host(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "host"},
		},
		NetworkSettings: &container.NetworkSettings{},
	}

	if got := GetContainerIPOnNetwork(containerInfo, "host"); got != "localhost" {
		t.Errorf("expected 'localhost', got '%s'", got)
	}
}

func TestGetContainerIPOnNetwork_Specific(t *testing.T) {
	containerInfo := &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{NetworkMode: "default"},
		},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"custom":  {IPAddress: "10.0.0.5"},
				"backend": {IPAddress: "10.0.0.10"},
			},
		},
	}

	if got := GetContainerIPOnNetwork(containerInfo, "custom"); got != "10.0.0.5" {
		t.Errorf("expected '10.0.0.5' from custom network, got '%s'", got)
	}
}

func TestGetContainerIPOnNetwork_MissingFallsBack(t *testing.T) {
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

	// Unknown network warns and falls back to default detection (bridge first).
	if got := GetContainerIPOnNetwork(containerInfo, "no-such-net"); got != "172.17.0.2" {
		t.Errorf("expected fallback '172.17.0.2', got '%s'", got)
	}
}

func TestAdaptDockTunnelToSpecs_Network(t *testing.T) {
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

	services := map[string]*ServiceConfig{
		"web": {Hostname: "example.com", Port: "8080", Scheme: "http", Network: "custom"},
	}

	specs := adaptDockTunnelToSpecs(services, containerInfo)
	web := specs["web"]
	if web == nil {
		t.Fatal("expected web spec")
	}
	if web.Network != "custom" {
		t.Errorf("expected Network 'custom', got '%s'", web.Network)
	}
	if web.ServiceURL != "http://10.0.0.5:8080" {
		t.Errorf("expected ServiceURL from custom network 'http://10.0.0.5:8080', got '%s'", web.ServiceURL)
	}
}

// --- A4: invalid docktunnel port must not panic and must not build a URL ---

func TestAdaptDockTunnelToSpecs_InvalidPortNoPanic(t *testing.T) {
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

	services := map[string]*ServiceConfig{
		"web": {Hostname: "example.com", Port: "not-a-port", Scheme: "http"},
	}

	specs := adaptDockTunnelToSpecs(services, containerInfo)
	web := specs["web"]
	if web == nil {
		t.Fatal("expected web spec")
	}
	if web.Port != 0 {
		t.Errorf("expected port 0 on parse failure, got %d", web.Port)
	}
	if web.ServiceURL != "" {
		t.Errorf("expected empty ServiceURL on parse failure, got '%s'", web.ServiceURL)
	}
}

func TestAdaptDockTunnelToSpecs_ServiceURL(t *testing.T) {
	services := map[string]*ServiceConfig{
		"web": {Hostname: "example.com", Service: "http://localhost:8080"},
	}

	specs := adaptDockTunnelToSpecs(services, nil)
	web := specs["web"]
	if web == nil {
		t.Fatal("expected web spec")
	}
	if web.ServiceURL != "http://localhost:8080" {
		t.Errorf("expected service URL 'http://localhost:8080', got '%s'", web.ServiceURL)
	}
}

func TestAdaptDockTunnelToSpecs_PortWithIP(t *testing.T) {
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

	services := map[string]*ServiceConfig{
		"web": {Hostname: "example.com", Port: "8080", Scheme: "https"},
	}

	specs := adaptDockTunnelToSpecs(services, containerInfo)
	web := specs["web"]
	if web.ServiceURL != "https://172.17.0.2:8080" {
		t.Errorf("expected 'https://172.17.0.2:8080', got '%s'", web.ServiceURL)
	}
}

func TestConvertOriginRequest(t *testing.T) {
	sc := &ServiceConfig{
		ConnectTimeout:       "30s",
		NoTLSVerify:          "true",
		KeepAliveConnections: "50",
		ProxyAddress:         "127.0.0.1",
		ProxyPort:            "9050",
		AccessRequired:       "true",
		AccessTeamName:       "my-team",
		AccessAUDTag:         "tag1, tag2",
	}

	spec := convertOriginRequest(sc)
	if spec == nil {
		t.Fatal("expected non-nil OriginRequestSpec")
	}

	if spec.ConnectTimeout == nil || *spec.ConnectTimeout != 30*time.Second {
		t.Errorf("expected connectTimeout 30s, got %v", spec.ConnectTimeout)
	}
	if spec.NoTLSVerify == nil || !*spec.NoTLSVerify {
		t.Error("expected noTLSVerify true")
	}
	if spec.KeepAliveConnections == nil || *spec.KeepAliveConnections != 50 {
		t.Errorf("expected keepAliveConnections 50, got %v", spec.KeepAliveConnections)
	}
	if spec.ProxyAddress != "127.0.0.1" {
		t.Errorf("expected proxyAddress '127.0.0.1', got '%s'", spec.ProxyAddress)
	}
	if spec.ProxyPort == nil || *spec.ProxyPort != 9050 {
		t.Errorf("expected proxyPort 9050, got %v", spec.ProxyPort)
	}
	if spec.MatchSNItoHost != nil {
		t.Error("matchSNItoHost is deprecated (no Cloudflare v5 API field) and must never be converted")
	}
	if spec.Access == nil {
		t.Fatal("expected access config")
	}
	if !spec.Access.Required {
		t.Error("expected access.required true")
	}
	if spec.Access.TeamName != "my-team" {
		t.Errorf("expected access.teamName 'my-team', got '%s'", spec.Access.TeamName)
	}
	if len(spec.Access.AUDTag) != 2 || spec.Access.AUDTag[0] != "tag1" {
		t.Errorf("expected access.audTag [tag1, tag2], got %v", spec.Access.AUDTag)
	}
}

// TestConvertOriginRequest_BoolLabelVariants verifies that boolean labels
// accept all the forms strconv.ParseBool supports (1, T, TRUE, True, etc.).
// This used to be `v == "true"`, which silently dropped True/TRUE/1.
func TestConvertOriginRequest_BoolLabelVariants(t *testing.T) {
	cases := map[string]bool{
		"true":  true,
		"True":  true,
		"TRUE":  true,
		"1":     true,
		"t":     true,
		"T":     true,
		"false": false,
		"False": false,
		"0":     false,
		"f":     false,
	}

	for val, want := range cases {
		sc := &ServiceConfig{NoTLSVerify: val}
		spec := convertOriginRequest(sc)
		if spec == nil {
			t.Errorf("value %q: expected non-nil spec", val)
			continue
		}
		if spec.NoTLSVerify == nil {
			t.Errorf("value %q: expected NoTLSVerify pointer to be set", val)
			continue
		}
		if *spec.NoTLSVerify != want {
			t.Errorf("value %q: expected NoTLSVerify=%v, got %v", val, want, *spec.NoTLSVerify)
		}
	}
}

// TestConvertOriginRequest_InvalidDurationDropsField ensures invalid
// durations are now logged and dropped, rather than silently propagated.
func TestConvertOriginRequest_InvalidDurationDropsField(t *testing.T) {
	sc := &ServiceConfig{
		ConnectTimeout: "not-a-duration",
		NoTLSVerify:    "true",
	}
	spec := convertOriginRequest(sc)
	if spec == nil {
		t.Fatal("expected non-nil spec (noTLSVerify is still valid)")
	}
	if spec.ConnectTimeout != nil {
		t.Errorf("expected ConnectTimeout to be dropped, got %v", *spec.ConnectTimeout)
	}
	if spec.NoTLSVerify == nil || !*spec.NoTLSVerify {
		t.Error("expected NoTLSVerify to still be set")
	}
}

// TestConvertOriginRequest_InvalidBoolDropsField ensures invalid booleans
// are dropped (not silently treated as false). With only one invalid field,
// hasAny stays false and convertOriginRequest returns nil.
func TestConvertOriginRequest_InvalidBoolDropsField(t *testing.T) {
	sc := &ServiceConfig{NoTLSVerify: "yes"}
	spec := convertOriginRequest(sc)
	if spec != nil {
		t.Errorf("expected nil spec when the only field is an invalid bool, got %+v", spec)
	}

	// Now verify that a valid field alongside an invalid bool still works,
	// and the invalid one is dropped rather than treated as false.
	sc2 := &ServiceConfig{NoTLSVerify: "yes", HTTP2Origin: "true"}
	spec2 := convertOriginRequest(sc2)
	if spec2 == nil {
		t.Fatal("expected non-nil spec (http2Origin is valid)")
	}
	if spec2.NoTLSVerify != nil {
		t.Errorf("expected NoTLSVerify to be dropped, got %v", *spec2.NoTLSVerify)
	}
	if spec2.HTTP2Origin == nil || !*spec2.HTTP2Origin {
		t.Error("expected HTTP2Origin to still be set")
	}
}

func TestMergeSpecs_DockTunnelOverridesTraefik(t *testing.T) {
	traefikSpecs := map[string]*IngressSpec{
		"myapp@example.com": {Hostname: "example.com", Port: 8080, Scheme: "http"},
	}
	docktunnelSpecs := map[string]*IngressSpec{
		"web": {Hostname: "example.com", ServiceURL: "http://localhost:3000"},
	}

	merged := mergeSpecs(traefikSpecs, docktunnelSpecs)

	if len(merged) != 1 {
		t.Fatalf("expected 1 merged spec, got %d", len(merged))
	}

	if _, ok := merged["web"]; !ok {
		t.Error("expected docktunnel 'web' key")
	}

	if _, ok := merged["myapp@example.com"]; ok {
		t.Error("traefik entry should be removed on hostname conflict")
	}
}

func TestMergeSpecs_NoConflict(t *testing.T) {
	traefikSpecs := map[string]*IngressSpec{
		"myapp@a.com": {Hostname: "a.com", Port: 8080},
	}
	docktunnelSpecs := map[string]*IngressSpec{
		"web": {Hostname: "b.com", ServiceURL: "http://localhost:3000"},
	}

	merged := mergeSpecs(traefikSpecs, docktunnelSpecs)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged specs, got %d", len(merged))
	}
}

// TestMergeSpecs_CaseInsensitiveConflict verifies that hostnames differing
// only in case collide, and that the surviving hostname is lower-cased (A3).
func TestMergeSpecs_CaseInsensitiveConflict(t *testing.T) {
	traefikSpecs := map[string]*IngressSpec{
		"http:myapp@Example.COM": {Hostname: "Example.COM", Port: 8080},
	}
	docktunnelSpecs := map[string]*IngressSpec{
		"web": {Hostname: "example.com", ServiceURL: "http://localhost:3000"},
	}

	merged := mergeSpecs(traefikSpecs, docktunnelSpecs)

	if len(merged) != 1 {
		t.Fatalf("expected 1 merged spec (case-insensitive conflict), got %d", len(merged))
	}
	web := merged["web"]
	if web == nil {
		t.Fatalf("expected docktunnel 'web' key to win, got keys: %v", mapKeys(merged))
	}
	if web.Hostname != "example.com" {
		t.Errorf("expected lower-cased hostname 'example.com', got '%s'", web.Hostname)
	}
	if _, ok := merged["http:myapp@Example.COM"]; ok {
		t.Error("traefik entry should be removed on case-insensitive hostname conflict")
	}
}

func TestBuildServiceURL_Specified(t *testing.T) {
	spec := &IngressSpec{ServiceURL: "http://localhost:8080"}
	result := buildServiceURL(spec, "172.17.0.2")
	if result != "http://localhost:8080" {
		t.Errorf("expected 'http://localhost:8080', got '%s'", result)
	}
}

func TestBuildServiceURL_FromPort(t *testing.T) {
	spec := &IngressSpec{Scheme: "https", Port: 8443}
	result := buildServiceURL(spec, "172.17.0.2")
	if result != "https://172.17.0.2:8443" {
		t.Errorf("expected 'https://172.17.0.2:8443', got '%s'", result)
	}
}

func TestBuildServiceURL_DefaultHTTP(t *testing.T) {
	spec := &IngressSpec{Port: 8080}
	result := buildServiceURL(spec, "172.17.0.2")
	if result != "http://172.17.0.2:8080" {
		t.Errorf("expected 'http://172.17.0.2:8080', got '%s'", result)
	}
}
