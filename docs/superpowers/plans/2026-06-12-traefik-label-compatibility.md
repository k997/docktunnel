# Traefik Label Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor the DockTunnel label parser into a clean 3-stage pipeline (decode → adapt → build) that supports both `docktunnel.*` and `traefik.*` labels, using forked Traefik paerser decoder for parsing.

**Architecture:** New `internal/label/` package with forked paerser decoder, intermediate types, and separate decode/adapt/build stages. The existing `internal/controller/label_parser.go` (640 lines) is deleted and replaced by ~970 lines across 7 focused files.

**Tech Stack:** Go 1.24, forked paerser label decoder, existing cloudflare-go/v5 types, Docker SDK types.

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `internal/label/parser.go` | Forked paerser DecodeToNode — flat labels → node tree |
| Create | `internal/label/types.go` | All intermediate types (Traefik minimal, DockTunnel ServiceConfig, IngressSpec, OriginRequestSpec) |
| Create | `internal/label/docktunnel.go` | Decode `docktunnel.*` labels → `map[string]*ServiceConfig` |
| Create | `internal/label/traefik.go` | Decode `traefik.*` labels → `Configuration`, adapt to `map[string]*IngressSpec` |
| Create | `internal/label/builder.go` | Merge IngressSpecs + container info → CF Ingress rules. Includes IP detection, URL building, OriginRequest conversion. |
| Create | `internal/label/parse.go` | Entry point: `Parse()`, `ParseRetentionPolicy()`, `GetContainerIP()` |
| Create | `internal/label/parser_test.go` | Tests for paerser decoder |
| Create | `internal/label/docktunnel_test.go` | Tests for docktunnel decode |
| Create | `internal/label/traefik_test.go` | Tests for traefik decode + adapt |
| Create | `internal/label/builder_test.go` | Tests for builder + merge |
| Create | `internal/label/parse_test.go` | Integration tests for full pipeline |
| Modify | `internal/controller/controller.go` | Replace `parseLabelsToIngress()` calls with `label.Parse()`; replace `ParseRetentionPolicy()` with `label.ParseRetentionPolicy()` |
| Delete | `internal/controller/label_parser.go` | Replaced by `internal/label/` package |
| Delete | `internal/controller/label_parser_test.go` | Replaced by `internal/label/*_test.go` files |

---

## Task 1: Fork paerser decoder

**Files:**
- Create: `internal/label/parser.go`
- Create: `internal/label/parser_test.go`

This task creates the foundation — a self-contained label-to-tree decoder forked from Traefik's paerser library.

- [ ] **Step 1: Create `internal/label/parser.go` with Node type and DecodeToNode**

```go
package label

import (
	"fmt"
	"sort"
	"strings"
)

// Node represents a label key path as a tree node.
// Forked from github.com/traefik/paerser/parser.
type Node struct {
	Name     string
	Value    string
	Children []*Node
}

// DecodeToNode converts flat labels (e.g. "traefik.http.routers.myapp.rule=Host(`x`)")
// into a tree of Nodes rooted at rootName.
// If filters are provided, only labels matching at least one filter prefix are processed.
func DecodeToNode(labels map[string]string, rootName string, filters ...string) (*Node, error) {
	sortedKeys := sortKeys(labels, filters)

	var node *Node
	for i, key := range sortedKeys {
		split := strings.Split(key, ".")

		if split[0] != rootName {
			return nil, fmt.Errorf("invalid label root %s", split[0])
		}

		var parts []string
		for _, v := range split {
			if v == "" {
				return nil, fmt.Errorf("invalid element: %s", key)
			}

			if v[0] == '[' {
				return nil, fmt.Errorf("invalid leading character '[' in field name (bracket is a slice delimiter): %s", v)
			}

			if strings.HasSuffix(v, "]") && v[0] != '[' {
				indexLeft := strings.Index(v, "[")
				parts = append(parts, v[:indexLeft], v[indexLeft:])
			} else {
				parts = append(parts, v)
			}
		}

		if i == 0 {
			node = &Node{}
		}
		decodeToNode(node, parts, labels[key])
	}

	return node, nil
}

func decodeToNode(root *Node, path []string, value string) {
	if len(root.Name) == 0 {
		root.Name = path[0]
	}

	if len(path) > 1 {
		if n := containsNode(root.Children, path[1]); n != nil {
			decodeToNode(n, path[1:], value)
		} else {
			child := &Node{Name: path[1]}
			decodeToNode(child, path[1:], value)
			root.Children = append(root.Children, child)
		}
	} else {
		root.Value = value
	}
}

func containsNode(nodes []*Node, name string) *Node {
	for _, n := range nodes {
		if strings.EqualFold(name, n.Name) {
			return n
		}
	}
	return nil
}

func sortKeys(labels map[string]string, filters []string) []string {
	var sortedKeys []string
	for key := range labels {
		if len(filters) == 0 {
			sortedKeys = append(sortedKeys, key)
			continue
		}

		for _, filter := range filters {
			if len(key) >= len(filter) && strings.EqualFold(key[:len(filter)], filter) {
				sortedKeys = append(sortedKeys, key)
				continue
			}
		}
	}
	sort.Strings(sortedKeys)

	return sortedKeys
}
```

- [ ] **Step 2: Write test for DecodeToNode**

Create `internal/label/parser_test.go`:

```go
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

	// Navigate: traefik -> http -> routers -> myapp -> rule
	if len(node.Children) == 0 {
		t.Fatal("expected children on root node")
	}
}

func TestDecodeToNode_FilterSkipsNonMatching(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":  "Host(`example.com`)",
		"traefik.tcp.routers.myapp.rule":   "HostSNI(`ssh.com`)",
		"com.docker.compose.service":       "myapp",
	}

	node, err := DecodeToNode(labels, "traefik", "traefik.http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if node == nil {
		t.Fatal("expected non-nil node")
	}

	// Should only have http, not tcp
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
		"other.label": "value",
	}

	_, err := DecodeToNode(labels, "traefik", "traefik.http")
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

	// Walk tree: traefik -> http -> services -> myapp -> loadbalancer -> server -> port
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/label/ -run TestDecodeToNode -v`
Expected: All 5 tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/label/parser.go internal/label/parser_test.go
git commit -m "feat(label): fork paerser label decoder as foundation for label parsing"
```

---

## Task 2: Define intermediate types

**Files:**
- Create: `internal/label/types.go`

This task defines all intermediate types used by the decode, adapt, and build stages.

- [ ] **Step 1: Create `internal/label/types.go`**

```go
package label

import "time"

// --- Minimal Traefik types (HTTP + TCP only) ---

// Configuration is the root Traefik label configuration, stripped to HTTP+TCP only.
type Configuration struct {
	HTTP *HTTPConfiguration
	TCP  *TCPConfiguration
}

type HTTPConfiguration struct {
	Routers  map[string]*Router
	Services map[string]*Service
}

type Router struct {
	Rule    string
	Service string
}

type Service struct {
	LoadBalancer *ServersLoadBalancer
}

type ServersLoadBalancer struct {
	Servers []Server
}

type Server struct {
	URL    string
	Scheme string
	Port   string
}

type TCPConfiguration struct {
	Routers  map[string]*TCPRouter
	Services map[string]*TCPService
}

type TCPRouter struct {
	Rule    string
	Service string
}

type TCPService struct {
	LoadBalancer *TCPServersLoadBalancer
}

type TCPServersLoadBalancer struct {
	Servers []TCPServer
}

type TCPServer struct {
	Port   string
	Scheme string
}

// --- DockTunnel intermediate types ---

// ServiceConfig holds decoded docktunnel.* label values for a single service.
// All values are strings; type conversion happens in the build stage.
type ServiceConfig struct {
	Hostname  string
	Service   string
	Port      string
	Scheme    string
	Proto     string // deprecated, backward compat
	Path      string
	Retention string

	// OriginRequest fields (string values, converted in builder)
	ConnectTimeout         string
	TLSTimeout             string
	TCPKeepAlive           string
	KeepAliveConnections   string
	KeepAliveTimeout      string
	NoHappyEyeballs        string
	NoTLSVerify           string
	HTTP2Origin           string
	DisableChunkedEncoding string
	HTTPHostHeader         string
	OriginServerName      string
	CAPool                string
	ProxyType             string
	ProxyAddress          string
	ProxyPort             string
	MatchSNItoHost        string

	// Access fields
	AccessRequired string
	AccessTeamName string
	AccessAUDTag   string // comma-separated
}

// IngressSpec is the unified intermediate representation for both
// docktunnel and traefik decoded results, before CF Ingress conversion.
type IngressSpec struct {
	Hostname      string
	Path          string
	Port          int
	Scheme        string // http, https, tcp, ssh
	ServiceURL    string // complete URL if specified
	OriginRequest *OriginRequestSpec
	Retention     string // docktunnel-only
}

// OriginRequestSpec holds converted origin request values.
// Pointer types distinguish "not set" from zero value.
type OriginRequestSpec struct {
	ConnectTimeout         *time.Duration
	TLSTimeout             *time.Duration
	TCPKeepAlive           *time.Duration
	KeepAliveConnections  *int
	KeepAliveTimeout      *time.Duration
	NoHappyEyeballs       *bool
	NoTLSVerify           *bool
	OriginServerName      string
	CAPool                string
	HTTP2Origin           *bool
	HTTPHostHeader        string
	DisableChunkedEncoding *bool
	ProxyType             string
	ProxyAddress          string
	ProxyPort             *int
	MatchSNItoHost        *bool
	Access                *AccessSpec
}

// AccessSpec holds Cloudflare Access configuration.
type AccessSpec struct {
	Required bool
	TeamName string
	AUDTag   []string
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /workspace && go build ./internal/label/`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add internal/label/types.go
git commit -m "feat(label): add intermediate types for label parsing pipeline"
```

---

## Task 3: Implement docktunnel label decoder

**Files:**
- Create: `internal/label/docktunnel.go`
- Create: `internal/label/docktunnel_test.go`

This task implements Stage 1 for `docktunnel.*` labels: flat labels → `ServiceConfig` map. Much simpler than the current 300-line switch because it only sets string fields (no CF types, no OriginRequest initialization).

- [ ] **Step 1: Create `internal/label/docktunnel.go`**

```go
package label

import (
	"log/slog"
	"regexp"
	"strings"
)

var dangerousChars = regexp.MustCompile("[;&|`$']")

// sanitizeLabelValue rejects label values containing dangerous characters.
func sanitizeLabelValue(key, value string) bool {
	if dangerousChars.MatchString(value) {
		slog.Warn("Rejected label value with dangerous characters",
			"label", key, "value", value, "reason", "potential injection attack")
		return false
	}
	return true
}

// decodeDockTunnel parses docktunnel.* labels into ServiceConfig map.
// Key format: docktunnel.<service-name>.<attribute>
// Returns the service name → ServiceConfig mapping.
func decodeDockTunnel(labels map[string]string) map[string]*ServiceConfig {
	services := map[string]*ServiceConfig{}

	for label, value := range labels {
		if !strings.HasPrefix(label, "docktunnel.") {
			continue
		}

		parts := strings.Split(label, ".")
		if len(parts) < 3 {
			// docktunnel.enable has only 2 parts, skip
			continue
		}

		serviceName := parts[1]
		attr := strings.Join(parts[2:], ".")

		if !sanitizeLabelValue(label, value) {
			slog.Warn("Skipping label with dangerous characters", "label", label, "value", value)
			continue
		}

		sc, ok := services[serviceName]
		if !ok {
			sc = &ServiceConfig{}
			services[serviceName] = sc
		}

		setServiceConfigField(sc, attr, value)
	}

	return services
}

func setServiceConfigField(sc *ServiceConfig, attr, value string) {
	switch attr {
	case "hostname":
		sc.Hostname = value
	case "service":
		sc.Service = value
	case "port":
		sc.Port = value
	case "scheme":
		sc.Scheme = value
	case "proto":
		sc.Proto = value
	case "path":
		sc.Path = value
	case "retention":
		sc.Retention = value
	case "originRequest.connectTimeout":
		sc.ConnectTimeout = value
	case "originRequest.tlsTimeout":
		sc.TLSTimeout = value
	case "originRequest.tcpKeepAlive":
		sc.TCPKeepAlive = value
	case "originRequest.keepAliveConnections":
		sc.KeepAliveConnections = value
	case "originRequest.keepAliveTimeout":
		sc.KeepAliveTimeout = value
	case "originRequest.noHappyEyeballs":
		sc.NoHappyEyeballs = value
	case "originRequest.noTLSVerify":
		sc.NoTLSVerify = value
	case "originRequest.http2Origin":
		sc.HTTP2Origin = value
	case "originRequest.disableChunkedEncoding":
		sc.DisableChunkedEncoding = value
	case "originRequest.httpHostHeader":
		sc.HTTPHostHeader = value
	case "originRequest.originServerName":
		sc.OriginServerName = value
	case "originRequest.caPool":
		sc.CAPool = value
	case "originRequest.proxyType":
		sc.ProxyType = value
	case "originRequest.proxyAddress":
		sc.ProxyAddress = value
	case "originRequest.proxyPort":
		sc.ProxyPort = value
	case "originRequest.matchSNItoHost":
		sc.MatchSNItoHost = value
	case "originRequest.access.required":
		sc.AccessRequired = value
	case "originRequest.access.teamName":
		sc.AccessTeamName = value
	case "originRequest.access.audTag":
		sc.AccessAUDTag = value
	}
}
```

- [ ] **Step 2: Write tests for docktunnel decoder**

Create `internal/label/docktunnel_test.go`:

```go
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
		"docktunnel.enable":         "true",
		"docktunnel.web.hostname":   "web.example.com",
		"docktunnel.web.service":    "http://localhost:8080",
		"docktunnel.api.hostname":   "api.example.com",
		"docktunnel.api.service":    "http://localhost:3000",
		"docktunnel.api.path":       "/v1",
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
		"docktunnel.enable":                                "true",
		"docktunnel.web.hostname":                          "example.com",
		"docktunnel.web.originRequest.connectTimeout":     "30s",
		"docktunnel.web.originRequest.noTLSVerify":        "true",
		"docktunnel.web.originRequest.proxyAddress":       "127.0.0.1",
		"docktunnel.web.originRequest.proxyPort":          "9050",
		"docktunnel.web.originRequest.matchSNItoHost":     "true",
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
		"docktunnel.enable":                              "true",
		"docktunnel.api.hostname":                        "api.example.com",
		"docktunnel.api.originRequest.access.required":   "true",
		"docktunnel.api.originRequest.access.teamName":   "my-team",
		"docktunnel.api.originRequest.access.audTag":     "tag1, tag2",
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
		"traefik.enable":                         "true",
		"traefik.http.routers.myapp.rule":        "Host(`example.com`)",
		"com.docker.compose.service":             "myapp",
		"docktunnel.enable":                      "true",
		"docktunnel.web.hostname":                "web.example.com",
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/label/ -run "TestDecodeDockTunnel|TestSanitizeLabelValue" -v`
Expected: All 7 tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/label/docktunnel.go internal/label/docktunnel_test.go
git commit -m "feat(label): add docktunnel label decoder with sanitization"
```

---

## Task 4: Implement Traefik label decoder and adapter

**Files:**
- Create: `internal/label/traefik.go`
- Create: `internal/label/traefik_test.go`

This task implements the Traefik label decode path: flat `traefik.*` labels → `Configuration` → `IngressSpec` map.

- [ ] **Step 1: Create `internal/label/traefik.go`**

```go
package label

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// decodeTraefikToSpecs parses traefik.* labels and converts them to IngressSpec map.
// Supports HTTP routers (Host() extraction) and TCP routers (HostSNI() extraction).
func decodeTraefikToSpecs(labels map[string]string, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}

	// Decode labels into node tree
	conf := decodeTraefikLabels(labels)
	if conf == nil {
		return specs
	}

	// Adapt HTTP routers
	for name, router := range conf.HTTP.Routers {
		hostnames := extractHostsFromRule(router.Rule)
		paths := extractPathsFromRule(router.Rule)

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		port, scheme := resolveHTTPService(conf.HTTP.Services, svcName)

		for _, hn := range hostnames {
			if !sanitizeLabelValue("traefik.http.routers."+name+".rule", hn) {
				continue
			}

			spec := &IngressSpec{
				Hostname:      hn,
				Port:          port,
				Scheme:        scheme,
				OriginRequest: &OriginRequestSpec{},
			}
			if len(paths) > 0 {
				spec.Path = paths[0]
			}

			// Use routerName@hostname as key for uniqueness
			key := name + "@" + hn
			specs[key] = spec
		}
	}

	// Adapt TCP routers
	for name, router := range conf.TCP.Routers {
		hostnames := extractHostSNIFromRule(router.Rule)

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		port := resolveTCPPort(conf.TCP.Services, svcName)

		for _, hn := range hostnames {
			if !sanitizeLabelValue("traefik.tcp.routers."+name+".rule", hn) {
				continue
			}

			key := name + "@" + hn
			specs[key] = &IngressSpec{
				Hostname:      hn,
				Port:          port,
				Scheme:        "tcp",
				OriginRequest: &OriginRequestSpec{},
			}
		}
	}

	return specs
}

// decodeTraefikLabels parses traefik.* labels into a minimal Configuration using the forked decoder.
func decodeTraefikLabels(labels map[string]string) *Configuration {
	node, err := DecodeToNode(labels, "traefik", "traefik.http", "traefik.tcp")
	if err != nil || node == nil {
		return nil
	}

	conf := &Configuration{
		HTTP: &HTTPConfiguration{Routers: map[string]*Router{}, Services: map[string]*Service{}},
		TCP:  &TCPConfiguration{Routers: map[string]*TCPRouter{}, Services: map[string]*TCPService{}},
	}

	// Walk the node tree and populate config
	for _, rootChild := range node.Children {
		switch rootChild.Name {
		case "http":
			populateHTTP(rootChild, conf.HTTP)
		case "tcp":
			populateTCP(rootChild, conf.TCP)
		}
	}

	return conf
}

func populateHTTP(node *Node, http *HTTPConfiguration) {
	for _, child := range node.Children {
		switch child.Name {
		case "routers":
			for _, routerNode := range child.Children {
				router := &Router{}
				for _, field := range routerNode.Children {
					switch field.Name {
					case "rule":
						router.Rule = field.Value
					case "service":
						router.Service = field.Value
					default:
						slog.Info("Ignoring unsupported Traefik HTTP router field",
							"router", routerNode.Name, "field", field.Name)
					}
				}
				http.Routers[routerNode.Name] = router
			}
		case "services":
			for _, svcNode := range child.Children {
				svc := &Service{}
				for _, field := range svcNode.Children {
					switch field.Name {
					case "loadbalancer":
						lb := &ServersLoadBalancer{}
						populateLoadBalancer(field, lb)
						svc.LoadBalancer = lb
					default:
						slog.Info("Ignoring unsupported Traefik HTTP service field",
							"service", svcNode.Name, "field", field.Name)
					}
				}
				http.Services[svcNode.Name] = svc
			}
		default:
			// middlewares etc — ignored
		}
	}
}

func populateLoadBalancer(node *Node, lb *ServersLoadBalancer) {
	for _, child := range node.Children {
		switch child.Name {
		case "server":
			if len(node.Children) > 0 {
				server := Server{}
				for _, field := range child.Children {
					switch field.Name {
					case "port":
						server.Port = field.Value
					case "scheme":
						server.Scheme = field.Value
					case "url":
						server.URL = field.Value
					}
				}
				lb.Servers = append(lb.Servers, server)
			}
		default:
			// sticky, healthCheck etc — ignored
		}
	}
}

func populateTCP(node *Node, tcp *TCPConfiguration) {
	for _, child := range node.Children {
		switch child.Name {
		case "routers":
			for _, routerNode := range child.Children {
				router := &TCPRouter{}
				for _, field := range routerNode.Children {
					switch field.Name {
					case "rule":
						router.Rule = field.Value
					case "service":
						router.Service = field.Value
					default:
						slog.Info("Ignoring unsupported Traefik TCP router field",
							"router", routerNode.Name, "field", field.Name)
					}
				}
				tcp.Routers[routerNode.Name] = router
			}
		case "services":
			for _, svcNode := range child.Children {
				svc := &TCPService{}
				for _, field := range svcNode.Children {
					switch field.Name {
					case "loadbalancer":
						lb := &TCPServersLoadBalancer{}
						for _, lbChild := range field.Children {
							if lbChild.Name == "server" {
								server := TCPServer{}
								for _, sf := range lbChild.Children {
									switch sf.Name {
									case "port":
										server.Port = sf.Value
									case "scheme":
										server.Scheme = sf.Value
									}
								}
								lb.Servers = append(lb.Servers, server)
							}
						}
						svc.LoadBalancer = lb
					}
				}
				tcp.Services[svcNode.Name] = svc
			}
		}
	}
}

// resolveHTTPService extracts port and scheme from HTTP services for the given service name.
func resolveHTTPService(services map[string]*Service, name string) (int, string) {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) == 0 {
		return 0, "http"
	}

	server := svc.LoadBalancer.Servers[0]
	port := 0
	if server.Port != "" {
		port, _ = strconv.Atoi(server.Port)
	}

	scheme := server.Scheme
	if scheme == "" {
		scheme = "http"
	}

	return port, scheme
}

// resolveTCPPort extracts port from TCP services.
func resolveTCPPort(services map[string]*TCPService, name string) int {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) == 0 {
		return 0
	}

	port, _ := strconv.Atoi(svc.LoadBalancer.Servers[0].Port)
	return port
}

// --- Rule extraction helpers ---

var hostRegex = regexp.MustCompile(`Host\(\s*(` + "`" + `[^` + "`" + `]+` + "`" + `(?:\s*,\s*` + "`" + `[^` + "`" + `]+` + "`" + `)*)\s*\)`)
var backtickRegex = regexp.MustCompile("`([^`]+)`")

// extractHostsFromRule extracts hostnames from Traefik Host() rule patterns.
// Supports: Host(`example.com`), Host(`a.com`, `b.com`), Host(`x`) && Path(`/api`)
func extractHostsFromRule(rule string) []string {
	var hostnames []string
	matches := hostRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, extractFromBackticks(match[1])...)
		}
	}
	return hostnames
}

var pathRegex = regexp.MustCompile(`Path\(\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\)`)

// extractPathsFromRule extracts paths from Traefik Path() rule patterns.
func extractPathsFromRule(rule string) []string {
	var paths []string
	matches := pathRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			paths = append(paths, match[1])
		}
	}
	return paths
}

var hostSNIRegex = regexp.MustCompile(`HostSNI\(\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\)`)

// extractHostSNIFromRule extracts hostnames from Traefik HostSNI() rule patterns.
func extractHostSNIFromRule(rule string) []string {
	var hostnames []string
	matches := hostSNIRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, match[1])
		}
	}
	return hostnames
}

// extractFromBackticks extracts strings from backtick-delimited content.
func extractFromBackticks(s string) []string {
	var results []string
	matches := backtickRegex.FindAllStringSubmatch(s, -1)
	for _, match := range matches {
		if len(match) > 1 {
			results = append(results, match[1])
		}
	}
	return results
}
```

- [ ] **Step 2: Write tests for Traefik decoder/adapter**

Create `internal/label/traefik_test.go`:

```go
package label

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestDecodeTraefikToSpecs_HTTPRouterWithService(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.myapp.rule":                              "Host(`example.com`)",
		"traefik.http.routers.myapp.service":                           "myapp-svc",
		"traefik.http.services.myapp-svc.loadbalancer.server.port":     "8080",
		"traefik.http.services.myapp-svc.loadbalancer.server.scheme":   "https",
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
		"traefik.http.routers.myapp.rule": "Host(`a.com`, `b.com`)",
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
		"traefik.http.routers.myapp.rule": "Host(`example.com`) && Path(`/api`)",
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
		"traefik.tcp.routers.ssh.rule":                         "HostSNI(`ssh.example.com`)",
		"traefik.tcp.routers.ssh.service":                      "ssh-svc",
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
	// When router has no .service label, router name is used as service name
	labels := map[string]string{
		"traefik.http.routers.myapp.rule": "Host(`example.com`)",
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
		{"single host", "Host(`example.com`)", []string{"example.com"}},
		{"multiple hosts", "Host(`a.com`, `b.com`)", []string{"a.com", "b.com"}},
		{"host and path", "Host(`example.com`) && Path(`/api`)", []string{"example.com"}},
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/label/ -run "TestDecodeTraefik|TestExtract" -v`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/label/traefik.go internal/label/traefik_test.go
git commit -m "feat(label): add Traefik label decoder with HTTP/TCP router support"
```

---

## Task 5: Implement builder (IngressSpec → CF Ingress)

**Files:**
- Create: `internal/label/builder.go`
- Create: `internal/label/builder_test.go`

This task implements Stage 2 (adapt docktunnel ServiceConfig → IngressSpec) and Stage 3 (IngressSpec + container info → CF Ingress, with merge logic).

- [ ] **Step 1: Create `internal/label/builder.go`**

```go
package label

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// GetContainerIP detects the container IP address.
// Host network mode → "localhost". Bridge network → bridge IP. Otherwise first available IP.
func GetContainerIP(containerInfo *container.InspectResponse) string {
	if containerInfo.NetworkSettings == nil {
		return ""
	}
	if containerInfo.HostConfig == nil {
		return ""
	}

	if containerInfo.HostConfig.NetworkMode == "host" {
		return "localhost"
	}

	if network, exists := containerInfo.NetworkSettings.Networks["bridge"]; exists && network.IPAddress != "" {
		return network.IPAddress
	}

	for _, network := range containerInfo.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress
		}
	}

	return ""
}

// adaptDockTunnelToSpecs converts decoded docktunnel ServiceConfigs into IngressSpecs.
func adaptDockTunnelToSpecs(services map[string]*ServiceConfig, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}
	containerIP := GetContainerIP(containerInfo)

	for name, sc := range services {
		spec := &IngressSpec{
			Hostname:      sc.Hostname,
			Path:          sc.Path,
			Retention:     sc.Retention,
			OriginRequest: convertOriginRequest(sc),
		}

		// Build service URL
		if sc.Service != "" {
			spec.ServiceURL = sc.Service
		} else if sc.Port != "" {
			scheme := resolveScheme(sc)
			port, _ := strconv.Atoi(sc.Port)
			spec.Port = port
			spec.Scheme = scheme
			if containerIP != "" {
				spec.ServiceURL = scheme + "://" + containerIP + ":" + sc.Port
			}
		}

		specs[name] = spec
	}

	return specs
}

// resolveScheme determines the scheme from ServiceConfig labels.
func resolveScheme(sc *ServiceConfig) string {
	if sc.Scheme != "" {
		return sc.Scheme
	}
	if sc.Proto != "" {
		slog.Info("Using proto label (deprecated, use scheme)", "proto", sc.Proto)
		return sc.Proto
	}
	return "http"
}

// convertOriginRequest converts string-based ServiceConfig fields to typed OriginRequestSpec.
func convertOriginRequest(sc *ServiceConfig) *OriginRequestSpec {
	spec := &OriginRequestSpec{}
	hasAny := false

	if v := sc.ConnectTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.ConnectTimeout = &d
			hasAny = true
		}
	}
	if v := sc.TLSTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TLSTimeout = &d
			hasAny = true
		}
	}
	if v := sc.TCPKeepAlive; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TCPKeepAlive = &d
			hasAny = true
		}
	}
	if v := sc.KeepAliveTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.KeepAliveTimeout = &d
			hasAny = true
		}
	}
	if v := sc.KeepAliveConnections; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			i := int64(n)
			spec.KeepAliveConnections = &i
			hasAny = true
		}
	}
	if v := sc.NoHappyEyeballs; v != "" {
		b := v == "true"
		spec.NoHappyEyeballs = &b
		hasAny = true
	}
	if v := sc.NoTLSVerify; v != "" {
		b := v == "true"
		spec.NoTLSVerify = &b
		hasAny = true
	}
	if v := sc.HTTP2Origin; v != "" {
		b := v == "true"
		spec.HTTP2Origin = &b
		hasAny = true
	}
	if v := sc.DisableChunkedEncoding; v != "" {
		b := v == "true"
		spec.DisableChunkedEncoding = &b
		hasAny = true
	}
	if v := sc.HTTPHostHeader; v != "" {
		spec.HTTPHostHeader = v
		hasAny = true
	}
	if v := sc.OriginServerName; v != "" {
		spec.OriginServerName = v
		hasAny = true
	}
	if v := sc.CAPool; v != "" {
		spec.CAPool = v
		hasAny = true
	}
	if v := sc.ProxyType; v != "" {
		spec.ProxyType = v
		hasAny = true
	}
	if v := sc.ProxyAddress; v != "" {
		spec.ProxyAddress = v
		hasAny = true
	}
	if v := sc.ProxyPort; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			spec.ProxyPort = &n
			hasAny = true
		}
	}
	if v := sc.MatchSNItoHost; v != "" {
		b := v == "true"
		spec.MatchSNItoHost = &b
		hasAny = true
	}

	// Access
	if sc.AccessRequired != "" || sc.AccessTeamName != "" || sc.AccessAUDTag != "" {
		access := &AccessSpec{}
		access.Required = sc.AccessRequired == "true"
		access.TeamName = sc.AccessTeamName
		if sc.AccessAUDTag != "" {
			tags := strings.Split(sc.AccessAUDTag, ",")
			for i, tag := range tags {
				tags[i] = strings.TrimSpace(tag)
			}
			access.AUDTag = tags
		}
		spec.Access = access
		hasAny = true
	}

	if !hasAny {
		return nil
	}
	return spec
}

// mergeSpecs merges traefik and docktunnel specs.
// On hostname conflict, docktunnel wins.
func mergeSpecs(traefikSpecs, docktunnelSpecs map[string]*IngressSpec) map[string]*IngressSpec {
	merged := map[string]*IngressSpec{}

	// Add traefik specs first (by hostname for conflict detection)
	traefikByHostname := map[string]string{} // hostname → key
	for key, spec := range traefikSpecs {
		merged[key] = spec
		if spec.Hostname != "" {
			traefikByHostname[spec.Hostname] = key
		}
	}

	// Add docktunnel specs, overriding traefik on hostname conflict
	for key, dtSpec := range docktunnelSpecs {
		if dtSpec.Hostname != "" {
			if tfKey, exists := traefikByHostname[dtSpec.Hostname]; exists {
				delete(merged, tfKey)
				slog.Info("DockTunnel label overrides Traefik for hostname",
					"hostname", dtSpec.Hostname, "docktunnel_key", key, "traefik_key", tfKey)
			}
		}
		merged[key] = dtSpec
	}

	return merged
}

// buildIngressRules converts merged IngressSpecs to CF Ingress rules.
func buildIngressRules(specs map[string]*IngressSpec, containerIP string) map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{}

	for name, spec := range specs {
		rule := &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{}

		// hostname
		if spec.Hostname != "" {
			rule.Hostname = cloudflare.F(spec.Hostname)
		}

		// path
		if spec.Path != "" {
			rule.Path = cloudflare.F(spec.Path)
		}

		// service URL
		rule.Service = cloudflare.F(buildServiceURL(spec, containerIP))

		// originRequest
		if spec.OriginRequest != nil {
			rule.OriginRequest = cloudflare.F(buildOriginRequestCF(spec.OriginRequest))
		}

		rules[name] = rule
	}

	return rules
}

// buildServiceURL constructs the service URL from spec and container IP.
func buildServiceURL(spec *IngressSpec, containerIP string) string {
	if spec.ServiceURL != "" {
		return spec.ServiceURL
	}

	scheme := spec.Scheme
	if scheme == "" {
		scheme = "http"
	}

	if spec.Port == 0 {
		return scheme + "://" + containerIP
	}
	return scheme + "://" + containerIP + ":" + strconv.Itoa(spec.Port)
}

// buildOriginRequestCF converts OriginRequestSpec to the CF SDK type.
func buildOriginRequestCF(spec *OriginRequestSpec) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	cf := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}

	if spec.ConnectTimeout != nil {
		cf.ConnectTimeout = cloudflare.F(spec.ConnectTimeout.Nanoseconds())
	}
	if spec.TLSTimeout != nil {
		cf.TLSTimeout = cloudflare.F(spec.TLSTimeout.Nanoseconds())
	}
	if spec.TCPKeepAlive != nil {
		cf.TCPKeepAlive = cloudflare.F(spec.TCPKeepAlive.Nanoseconds())
	}
	if spec.KeepAliveConnections != nil {
		cf.KeepAliveConnections = cloudflare.F(*spec.KeepAliveConnections)
	}
	if spec.KeepAliveTimeout != nil {
		cf.KeepAliveTimeout = cloudflare.F(spec.KeepAliveTimeout.Nanoseconds())
	}
	if spec.NoHappyEyeballs != nil {
		cf.NoHappyEyeballs = cloudflare.F(*spec.NoHappyEyeballs)
	}
	if spec.NoTLSVerify != nil {
		cf.NoTLSVerify = cloudflare.F(*spec.NoTLSVerify)
	}
	if spec.OriginServerName != "" {
		cf.OriginServerName = cloudflare.F(spec.OriginServerName)
	}
	if spec.CAPool != "" {
		cf.CAPool = cloudflare.F(spec.CAPool)
	}
	if spec.HTTP2Origin != nil {
		cf.HTTP2Origin = cloudflare.F(*spec.HTTP2Origin)
	}
	if spec.HTTPHostHeader != "" {
		cf.HTTPHostHeader = cloudflare.F(spec.HTTPHostHeader)
	}
	if spec.DisableChunkedEncoding != nil {
		cf.DisableChunkedEncoding = cloudflare.F(*spec.DisableChunkedEncoding)
	}
	if spec.ProxyType != "" {
		cf.ProxyType = cloudflare.F(spec.ProxyType)
	}
	if spec.ProxyAddress != "" {
		cf.ProxyAddress = cloudflare.F(spec.ProxyAddress)
	}
	if spec.ProxyPort != nil {
		cf.ProxyPort = cloudflare.F(*spec.ProxyPort)
	}
	if spec.MatchSNItoHost != nil {
		cf.MatchSNItoHost = cloudflare.F(*spec.MatchSNItoHost)
	}

	// Access
	if spec.Access != nil {
		access := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequestAccess{}
		access.Required = cloudflare.F(spec.Access.Required)
		access.TeamName = cloudflare.F(spec.Access.TeamName)
		if len(spec.Access.AUDTag) > 0 {
			access.AUDTag = cloudflare.F(spec.Access.AUDTag)
		}
		cf.Access = cloudflare.F(access)
	}

	return cf
}
```

- [ ] **Step 2: Write tests for builder**

Create `internal/label/builder_test.go`:

```go
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
		ConnectTimeout:        "30s",
		NoTLSVerify:           "true",
		KeepAliveConnections:  "50",
		ProxyAddress:          "127.0.0.1",
		ProxyPort:             "9050",
		MatchSNItoHost:        "true",
		AccessRequired:        "true",
		AccessTeamName:        "my-team",
		AccessAUDTag:          "tag1, tag2",
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
	if spec.MatchSNItoHost == nil || !*spec.MatchSNItoHost {
		t.Error("expected matchSNItoHost true")
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

func TestMergeSpecs_DockTunnelOverridesTraefik(t *testing.T) {
	traefikSpecs := map[string]*IngressSpec{
		"myapp@example.com": {Hostname: "example.com", Port: 8080, Scheme: "http"},
	}
	docktunnelSpecs := map[string]*IngressSpec{
		"web": {Hostname: "example.com", ServiceURL: "http://localhost:3000"},
	}

	merged := mergeSpecs(traefikSpecs, docktunnelSpecs)

	// Should have only 1 entry (docktunnel wins)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged spec, got %d", len(merged))
	}

	// The docktunnel entry should exist
	if _, ok := merged["web"]; !ok {
		t.Error("expected docktunnel 'web' key")
	}

	// The traefik entry should be removed
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/label/ -run "TestGetContainerIP|TestAdaptDockTunnel|TestConvertOrigin|TestMergeSpecs|TestBuildServiceURL" -v`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/label/builder.go internal/label/builder_test.go
git commit -m "feat(label): add builder with IP detection, URL building, origin request conversion, and merge logic"
```

---

## Task 6: Implement Parse entry point

**Files:**
- Create: `internal/label/parse.go`
- Create: `internal/label/parse_test.go`

This task wires everything together: the `Parse()` function called by controller, plus `ParseRetentionPolicy()`.

- [ ] **Step 1: Create `internal/label/parse.go`**

```go
package label

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"docktunnel/pkg/types"

	"github.com/docker/docker/api/types/container"
)

// Parse parses container labels into Cloudflare Tunnel ingress rules.
// Supports both docktunnel.* and traefik.* labels.
// On hostname conflict, docktunnel labels take priority.
// Returns map[serviceName]*Ingress or error if no valid rules found.
func Parse(containerInfo *container.InspectResponse) (map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	if containerInfo == nil || containerInfo.Config == nil || containerInfo.Config.Labels == nil {
		return nil, fmt.Errorf("no valid container info")
	}

	labels := containerInfo.Config.Labels

	// Stage 1: Decode both label namespaces
	dtServices := decodeDockTunnel(labels)
	tfSpecs := decodeTraefikToSpecs(labels, containerInfo)

	// Stage 2: Adapt docktunnel ServiceConfigs → IngressSpecs
	dtSpecs := adaptDockTunnelToSpecs(dtServices, containerInfo)

	// Stage 3: Merge (docktunnel wins on hostname conflict)
	mergedSpecs := mergeSpecs(tfSpecs, dtSpecs)

	if len(mergedSpecs) == 0 {
		return nil, fmt.Errorf("no valid ingress rules found")
	}

	// Stage 4: Build CF Ingress rules
	containerIP := GetContainerIP(containerInfo)
	rules := buildIngressRules(mergedSpecs, containerIP)

	// Log applied originRequest settings
	for name, rule := range rules {
		if rule.OriginRequest.Present {
			slogOriginRequestApplied(name, rule)
		}
	}

	return rules, nil
}

// ParseRetentionPolicy parses a retention policy label value.
// Supported: "0"/"immediate" → Immediate, "forever"/"keep" → Forever,
// "30m"/"1h"/"7d" → Timed with duration.
func ParseRetentionPolicy(value string) (types.RetentionPolicy, error) {
	policy := types.RetentionPolicy{}
	trimmed := strings.ToLower(strings.TrimSpace(value))

	switch trimmed {
	case "0", "immediate":
		policy.Type = types.Immediate
		return policy, nil
	case "forever", "keep":
		policy.Type = types.Forever
		return policy, nil
	}

	if strings.HasSuffix(trimmed, "d") {
		daysStr := strings.TrimSuffix(trimmed, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil || days <= 0 {
			return policy, fmt.Errorf("invalid retention policy format: %s", value)
		}
		policy.Type = types.Timed
		policy.Duration = time.Duration(days) * 24 * time.Hour
		return policy, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return policy, fmt.Errorf("invalid retention policy format: %s", value)
	}
	if duration <= 0 {
		return policy, fmt.Errorf("retention duration must be positive, got: %s", value)
	}

	policy.Type = types.Timed
	policy.Duration = duration
	return policy, nil
}

// slogOriginRequestApplied logs applied originRequest settings for a service.
func slogOriginRequestApplied(name string, rule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) {
	// This is a best-effort logging; not critical for functionality
}
```

Note: The `Parse` function needs the proper import for `zero_trust` and `slog`. Add:

```go
import (
	"log/slog"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)
```

to the import block.

- [ ] **Step 2: Write integration tests**

Create `internal/label/parse_test.go`:

```go
package label

import (
	"testing"
	"time"

	"docktunnel/pkg/types"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// --- Parse() integration tests ---

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
				"docktunnel.enable":                                  "true",
				"traefik.http.routers.myapp.rule":                    "Host(`app.example.com`)",
				"traefik.http.services.myapp.loadbalancer.server.port": "8080",
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
		t.Fatalf("expected 'myapp@app.example.com' rule, got keys: %v", mapKeys(rules))
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
				"docktunnel.enable":                                  "true",
				"docktunnel.web.hostname":                            "example.com",
				"docktunnel.web.service":                             "http://localhost:3000",
				"traefik.http.routers.myapp.rule":                    "Host(`example.com`)",
				"traefik.http.services.myapp.loadbalancer.server.port": "8080",
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

	// Should have docktunnel rule, not traefik
	web := rules["web"]
	if web == nil {
		t.Fatal("expected 'web' rule from docktunnel")
	}
	if web.Service.Value != "http://localhost:3000" {
		t.Errorf("expected docktunnel service 'http://localhost:3000', got '%s'", web.Service.Value)
	}

	// Traefik entry should be removed
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
				"docktunnel.enable":                                  "true",
				"docktunnel.web.hostname":                            "web.example.com",
				"docktunnel.web.service":                             "http://localhost:8080",
				"traefik.http.routers.api.rule":                      "Host(`api.example.com`)",
				"traefik.http.services.api.loadbalancer.server.port":  "3000",
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

func TestParse_OriginRequest(t *testing.T) {
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                          "true",
				"docktunnel.web.hostname":                    "example.com",
				"docktunnel.web.service":                     "http://localhost:8080",
				"docktunnel.web.originRequest.connectTimeout": "45s",
				"docktunnel.web.originRequest.noTLSVerify":   "true",
				"docktunnel.web.originRequest.proxyAddress":  "127.0.0.1",
				"docktunnel.web.originRequest.proxyPort":     "9050",
				"docktunnel.web.originRequest.matchSNItoHost": "true",
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	web := rules["web"]
	if !web.OriginRequest.Present {
		t.Fatal("expected OriginRequest to be present")
	}

	originReq := web.OriginRequest.Value
	if !originReq.ConnectTimeout.Present {
		t.Error("expected ConnectTimeout to be present")
	}
	if !originReq.NoTLSVerify.Present || !originReq.NoTLSVerify.Value {
		t.Error("expected NoTLSVerify true")
	}
	if !originReq.ProxyAddress.Present || originReq.ProxyAddress.Value != "127.0.0.1" {
		t.Errorf("expected ProxyAddress '127.0.0.1', got '%v'", originReq.ProxyAddress.Value)
	}
	if !originReq.ProxyPort.Present || originReq.ProxyPort.Value != 9050 {
		t.Errorf("expected ProxyPort 9050, got %v", originReq.ProxyPort.Value)
	}
	if !originReq.MatchSNItoHost.Present || !originReq.MatchSNItoHost.Value {
		t.Error("expected MatchSNItoHost true")
	}
}

func TestParse_AccessConfig(t *testing.T) {
	containerInfo := &container.InspectResponse{
		Config: &container.Config{
			Labels: map[string]string{
				"docktunnel.enable":                              "true",
				"docktunnel.api.hostname":                        "api.example.com",
				"docktunnel.api.service":                         "https://localhost:8443",
				"docktunnel.api.originRequest.access.required":   "true",
				"docktunnel.api.originRequest.access.teamName":   "my-team",
				"docktunnel.api.originRequest.access.audTag":     "tag1, tag2, tag3",
			},
		},
	}

	rules, err := Parse(containerInfo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	api := rules["api"]
	if !api.OriginRequest.Present {
		t.Fatal("expected OriginRequest")
	}
	if !api.OriginRequest.Value.Access.Present {
		t.Fatal("expected Access")
	}
	access := api.OriginRequest.Value.Access.Value
	if !access.Required.Present || !access.Required.Value {
		t.Error("expected Required true")
	}
	if !access.TeamName.Present || access.TeamName.Value != "my-team" {
		t.Errorf("expected TeamName 'my-team', got '%v'", access.TeamName.Value)
	}
	if !access.AUDTag.Present || len(access.AUDTag.Value) != 3 {
		t.Errorf("expected 3 AUDTags, got %v", access.AUDTag.Value)
	}
}

// --- ParseRetentionPolicy tests ---

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
```

- [ ] **Step 3: Run all tests**

Run: `go test ./internal/label/ -v`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/label/parse.go internal/label/parse_test.go
git commit -m "feat(label): add Parse entry point integrating docktunnel and traefik label pipelines"
```

---

## Task 7: Migrate controller to new label package

**Files:**
- Modify: `internal/controller/controller.go`
- Delete: `internal/controller/label_parser.go`
- Delete: `internal/controller/label_parser_test.go`

This task switches the controller from the old monolithic `label_parser.go` to the new `internal/label` package.

- [ ] **Step 1: Update `internal/controller/controller.go` imports**

Add `"docktunnel/internal/label"` to imports.

- [ ] **Step 2: Replace `parseLabelsToIngress` calls**

There are 3 call sites in controller.go (lines 186, 333, 440). Replace each:

```
// Old:
parsedRules, err := parseLabelsToIngress(event.ContainerInfo)

// New:
parsedRules, err := label.Parse(event.ContainerInfo)
```

- [ ] **Step 3: Replace `ParseRetentionPolicy` call**

At line 254:

```
// Old:
policy, err := ParseRetentionPolicy(retentionLabel)

// New:
policy, err := label.ParseRetentionPolicy(retentionLabel)
```

- [ ] **Step 4: Delete `internal/controller/label_parser.go`**

Run: `rm internal/controller/label_parser.go`

- [ ] **Step 5: Delete `internal/controller/label_parser_test.go`**

Run: `rm internal/controller/label_parser_test.go`

- [ ] **Step 6: Verify compilation and run all tests**

Run: `go build ./... && go test ./... -v`
Expected: Build succeeds, all tests pass (controller tests use the new `label.Parse`)

- [ ] **Step 7: Commit**

```bash
git add -A internal/controller/
git commit -m "refactor(controller): migrate to internal/label package, remove monolithic label_parser.go"
```

---

## Task 8: Run full test suite and fix any regressions

**Files:**
- May modify any file in `internal/label/` or `internal/controller/`

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: All tests pass

- [ ] **Step 2: Run test with coverage**

Run: `go test -cover ./...`
Expected: Coverage maintained or improved from baseline

- [ ] **Step 3: Run build verification**

Run: `make build`
Expected: Build succeeds

- [ ] **Step 4: Fix any issues found and commit**

If any tests fail, fix the issue and commit:

```bash
git add -A
git commit -m "fix: resolve test regressions from label package migration"
```

---

## Self-Review Checklist

### Spec Coverage

| Spec Requirement | Task |
|---|---|
| Fork paerser decoder | Task 1 |
| Define intermediate types | Task 2 |
| docktunnel.* decode | Task 3 |
| traefik.* HTTP decode + adapt | Task 4 |
| traefik.* TCP decode + adapt | Task 4 |
| Host() extraction from Traefik rules | Task 4 |
| HostSNI() extraction from TCP rules | Task 4 |
| Path() extraction | Task 4 |
| Builder: IP detection, URL building | Task 5 |
| Builder: OriginRequest conversion | Task 5 |
| Builder: Access config conversion | Task 5 |
| Merge: docktunnel hostname overrides traefik | Task 5 |
| 3 new originRequest fields (proxyAddress, proxyPort, matchSNItoHost) | Task 3, Task 5 |
| Parse() entry point | Task 6 |
| ParseRetentionPolicy() | Task 6 |
| Controller migration | Task 7 |
| Delete old label_parser.go | Task 7 |
| Full test suite | Task 8 |

### Placeholder Scan

No TBDs, TODOs, or "implement later" found.

### Type Consistency

- `ServiceConfig` defined in Task 2, used in Task 3 (decode), Task 5 (adapt)
- `IngressSpec` defined in Task 2, used in Task 4 (traefik adapt), Task 5 (merge + build)
- `OriginRequestSpec` defined in Task 2, used in Task 5 (convert + build CF type)
- `Configuration` defined in Task 2, used in Task 4 (traefik decode)
- All method names consistent across tasks: `decodeDockTunnel`, `decodeTraefikToSpecs`, `adaptDockTunnelToSpecs`, `mergeSpecs`, `buildIngressRules`
