# Container Label Schema Contract

**Feature**: Cloudflare Tunnel Docker Controller
**Purpose**: Define the complete label schema for container configuration
**Version**: 1.0

## Overview

This contract defines the complete Docker label schema for configuring Cloudflare Tunnel routes. Labels follow a hierarchical 4-layer priority system: DockTunnel → Traefik → Auto-detection → Global defaults.

---

## Label Syntax

### DockTunnel Label Format

```
docktunnel.<service-name>.<attribute>
```

**Components**:
- `docktunnel`: Fixed prefix (identifies labels for this controller)
- `<service-name>`: User-defined service identifier (e.g., `web`, `api`, `metrics`)
- `<attribute>`: Configuration attribute (e.g., `hostname`, `service`, `originRequest.noTLSVerify`)

**Examples**:
```bash
docktunnel.enable=true
docktunnel.web.hostname=app.example.com
docktunnel.web.service=http://localhost:8080
docktunnel.api.hostname=api.example.com
docktunnel.api.service=http://localhost:3000
docktunnel.api.path=/api
```

---

## Core Labels

### Enable Flag

**Label**: `docktunnel.enable`
**Type**: Boolean (`true`/`false`)
**Required**: Yes (gateway label)
**Default**: `false`
**Priority**: N/A (filter, not configuration)

**Description**: Container must have this label set to `true` to be processed by the controller.

**Example**:
```bash
docker run -d \
  -l docktunnel.enable=true \
  nginx:latest
```

**Validation**:
```go
if labels["docktunnel.enable"] != "true" {
    // Skip container
    return
}
```

---

## Service Configuration Labels

### Hostname

**Label**: `docktunnel.<name>.hostname`
**Type**: String (FQDN)
**Required**: Yes (for each service)
**Priority**: 1 (DockTunnel labels)

**Description**: External hostname for the service. Must be unique across all containers.

**Format**:
- Fully qualified domain name (e.g., `app.example.com`)
- No wildcards or subdomain wildcards
- Case-insensitive (stored as-is, normalized in comparisons)

**Example**:
```bash
docktunnel.web.hostname=app.example.com
```

**Validation** (FR-005):
```go
func validateHostname(hostname string) error {
    // Must not be empty
    if hostname == "" {
        return errors.New("hostname cannot be empty")
    }

    // Must be FQDN format
    if !strings.Contains(hostname, ".") {
        return errors.New("hostname must be FQDN")
    }

    // Check uniqueness across all containers
    return nil
}
```

---

### Service URL

**Label**: `docktunnel.<name>.service`
**Type**: URL string
**Required**: No (auto-detected if missing)
**Priority**: 1 (DockTunnel labels)

**Description**: Complete origin service URL. If specified, overrides all auto-detection (port, IP, protocol).

**Format**:
```
<scheme>://<host>:<port>
```

**Supported Schemes**: `http`, `https`, `tcp`, `unix`

**Example**:
```bash
docktunnel.web.service=http://172.17.0.5:8080
docktunnel.ssh.service=tcp://172.17.0.6:22
docktunnel.socket.service=unix:///var/run/app.sock
```

**Validation**:
```go
func validateServiceURL(serviceURL string) error {
    u, err := url.Parse(serviceURL)
    if err != nil {
        return fmt.Errorf("invalid service URL: %w", err)
    }

    if u.Scheme == "" {
        return errors.New("service URL must include scheme (http, https, tcp, unix)")
    }

    return nil
}
```

**Priority**: Overrides port, IP, and scheme detection (FR-006)

---

### Path

**Label**: `docktunnel.<name>.path`
**Type**: String
**Required**: No
**Default**: `/*` (all paths)
**Priority**: 1 (DockTunnel labels)

**Description**: URL path prefix for routing (e.g., `/api`, `/v1/*`).

**Format**:
- Must start with `/`
- Can include wildcard `/*` suffix
- Case-sensitive

**Example**:
```bash
docktunnel.api.path=/api
docktunnel.api.path=/v1/*
docktunnel.web.path=/*
```

**Cloudflare Mapping**:
```json
{
  "hostname": "api.example.com",
  "path": "/api/*",
  "service": "http://172.17.0.5:3000"
}
```

---

### Scheme

**Label**: `docktunnel.<name>.scheme`
**Type**: Enum (`http`, `https`)
**Required**: No
**Default**: `http`
**Priority**: 1 (DockTunnel labels)

**Description**: Protocol scheme for origin service. Only used if `service` URL is NOT specified.

**Example**:
```bash
docktunnel.web.scheme=https
```

**Usage** (FR-015):
```go
scheme := getScheme(labels)  // Returns "https" from label
ip := detectIPAddress(containerJSON)
port := detectPort(labels)
serviceURL := fmt.Sprintf("%s://%s:%d", scheme, ip, port)
```

---

### Port

**Label**: `docktunnel.<name>.port`
**Type**: Integer (1-65535)
**Required**: No
**Default**: First exposed Docker port (FR-009)
**Priority**: 1 (DockTunnel labels)

**Description**: Container port number. Only used if `service` URL is NOT specified.

**Example**:
```bash
docktunnel.web.port=8080
```

**Auto-Detection Fallback** (Priority 3):
```go
func detectPort(containerJSON *types.ContainerJSON) int {
    // Priority: label → Traefik label → exposed ports → default
    if port, exists := labels["docktunnel.web.port"]; exists {
        return strconv.Atoi(port)
    }

    if port, exists := labels["traefik.http.services.web.loadbalancer.server.port"]; exists {
        return strconv.Atoi(port)
    }

    for port := range containerJSON.Config.ExposedPorts {
        // Parse "8080/tcp" → 8080
        return parsePort(port)
    }

    return 80  // Default port
}
```

---

## Retention Policy Labels

### Delete Retention

**Label**: `docktunnel.delete_retention` (global) or `docktunnel.<name>.delete_retention` (per-service)
**Type**: String
**Required**: No
**Default**: `1h` (1 hour)
**Priority**: 1 (DockTunnel labels)

**Description**: How long to retain tunnel route after container stops.

**Values**:
| Value | Type | Description |
|-------|------|-------------|
| `0`, `immediate` | Immediate | Delete route immediately on container stop |
| `30m`, `1h`, `7d` | Timed | Retain for specified duration |
| `forever`, `keep` | Forever | Never auto-delete (manual deletion only) |

**Examples**:
```bash
# Global retention (applies to all services in container)
docktunnel.delete_retention=30m

# Per-service retention (overrides global)
docktunnel.web.delete_retention=immediate
docktunnel.api.delete_retention=forever
```

**Parsing** (FR-016):
```go
func parseRetentionPolicy(value string) (PolicyType, time.Duration, error) {
    switch strings.ToLower(value) {
    case "0", "immediate":
        return Immediate, 0, nil
    case "forever", "keep":
        return Forever, 0, nil
    default:
        // Parse duration (e.g., "30m", "1h", "7d")
        duration, err := time.ParseDuration(value)
        if err != nil {
            return Unknown, 0, fmt.Errorf("invalid retention policy: %s", value)
        }
        return Timed, duration, nil
    }
}
```

---

## Origin Request Labels

### TLS Settings

#### noTLSVerify

**Label**: `docktunnel.<name>.originRequest.noTLSVerify`
**Type**: Boolean
**Default**: `false`
**Description**: Skip TLS certificate verification (for self-signed certificates).

**Example**:
```bash
docktunnel.web.originRequest.noTLSVerify=true
```

#### originServerName

**Label**: `docktunnel.<name>.originRequest.originServerName`
**Type**: String (hostname)
**Description**: SNI hostname for TLS handshake.

**Example**:
```bash
docktunnel.web.originRequest.originServerName=origin.example.com
```

#### matchSniToHost

**Label**: `docktunnel.<name>.originRequest.matchSniToHost`
**Type**: Boolean
**Default**: `false`
**Description**: Automatically set SNI hostname from external hostname.

**Example**:
```bash
docktunnel.web.originRequest.matchSniToHost=true
```

#### caPool

**Label**: `docktunnel.<name>.originRequest.caPool`
**Type**: String (file path)
**Description**: Path to CA certificate bundle (file must exist in container).

**Example**:
```bash
docktunnel.web.originRequest.caPool=/etc/ssl/certs/ca-bundle.crt
```

---

### Timeout Settings

#### connectTimeout

**Label**: `docktunnel.<name>.originRequest.connectTimeout`
**Type**: Duration (e.g., `30s`, `1m`)
**Default**: `30s`
**Description**: TCP connection timeout.

**Example**:
```bash
docktunnel.web.originRequest.connectTimeout=30s
```

#### tlsTimeout

**Label**: `docktunnel.<name>.originRequest.tlsTimeout`
**Type**: Duration
**Default**: `10s`
**Description**: TLS handshake timeout.

**Example**:
```bash
docktunnel.web.originRequest.tlsTimeout=10s
```

---

### Connection Pool Settings

#### tcpKeepAlive

**Label**: `docktunnel.<name>.originRequest.tcpKeepAlive`
**Type**: Duration
**Default**: `30s`
**Description**: TCP keep-alive probe interval.

**Example**:
```bash
docktunnel.web.originRequest.tcpKeepAlive=60s
```

#### keepAliveConnections

**Label**: `docktunnel.<name>.originRequest.keepAliveConnections`
**Type**: Integer
**Default**: `100`
**Description**: Maximum idle connections to maintain.

**Example**:
```bash
docktunnel.web.originRequest.keepAliveConnections=50
```

#### keepAliveTimeout

**Label**: `docktunnel.<name>.originRequest.keepAliveTimeout`
**Type**: Duration
**Default**: `90s`
**Description**: Idle connection timeout before closing.

**Example**:
```bash
docktunnel.web.originRequest.keepAliveTimeout=2m
```

---

### HTTP/2 Settings

#### http2Origin

**Label**: `docktunnel.<name>.originRequest.http2Origin`
**Type**: Boolean
**Default**: `false`
**Description**: Enable HTTP/2 for origin connection (required for gRPC).

**Example**:
```bash
docktunnel.grpc.originRequest.http2Origin=true
```

#### disableChunkedEncoding

**Label**: `docktunnel.<name>.originRequest.disableChunkedEncoding`
**Type**: Boolean
**Default**: `false`
**Description**: Disable HTTP chunked transfer encoding.

**Example**:
```bash
docktunnel.web.originRequest.disableChunkedEncoding=true
```

---

### Proxy Settings

#### proxyType

**Label**: `docktunnel.<name>.originRequest.proxyType`
**Type**: Enum (`socks`)
**Default**: Empty (no proxy)
**Description**: Proxy protocol type.

**Example**:
```bash
docktunnel.web.originRequest.proxyType=socks
```

#### proxyAddress

**Label**: `docktunnel.<name>.originRequest.proxyAddress`
**Type**: String (IP or hostname)
**Description**: Proxy server address.

**Example**:
```bash
docktunnel.web.originRequest.proxyAddress=127.0.0.1
```

#### proxyPort

**Label**: `docktunnel.<name>.originRequest.proxyPort`
**Type**: Integer
**Description**: Proxy server port.

**Example**:
```bash
docktunnel.web.originRequest.proxyPort=1080
```

---

### HTTP Header Settings

#### httpHostHeader

**Label**: `docktunnel.<name>.originRequest.httpHostHeader`
**Type**: String
**Description**: Override Host header sent to origin (defaults to external hostname).

**Example**:
```bash
docktunnel.web.originRequest.httpHostHeader=internal.example.com
```

---

### Happy Eyeballs Algorithm

#### noHappyEyeballs

**Label**: `docktunnel.<name>.originRequest.noHappyEyeballs`
**Type**: Boolean
**Default**: `false`
**Description**: Disable Happy Eyeballs algorithm (IPv4/IPv6 fallback).

**Example**:
```bash
docktunnel.web.originRequest.noHappyEyeballs=true
```

---

## Cloudflare Access Labels

### Access Configuration

**Prefix**: `docktunnel.<name>.originRequest.access.*`

#### required

**Label**: `docktunnel.<name>.originRequest.access.required`
**Type**: Boolean
**Default**: `false`
**Description**: Require Cloudflare Access authentication.

**Example**:
```bash
docktunnel.web.originRequest.access.required=true
```

#### teamName

**Label**: `docktunnel.<name>.originRequest.access.teamName`
**Type**: String
**Description**: Cloudflare Zero Trust team name.

**Example**:
```bash
docktunnel.web.originRequest.access.teamName=my-team
```

#### audTag

**Label**: `docktunnel.<name>.originRequest.access.audTag`
**Type**: String
**Description**: Application Audience Tag for JWT validation.

**Example**:
```bash
docktunnel.web.originRequest.access.audTag=audTag-example
```

---

## Traefik Compatibility Labels

### Router Rules

**Label**: `traefik.http.routers.<name>.rule`
**Type**: String
**Priority**: 2 (Traefik labels)
**Description**: Traefik router rule with `Host()` and `Path()` patterns.

**Extraction Patterns**:
- `Host('hostname')` → Extract hostname
- `Host('hostname1', 'hostname2')` → Extract multiple hostnames
- `PathPrefix('/path')` → Extract path prefix

**Example**:
```bash
# Single hostname
traefik.http.routers.web.rule=Host(`app.example.com`)

# Multiple hostnames (creates multiple routes)
traefik.http.routers.web.rule=Host(`app.example.com`, `www.app.example.com`)

# Hostname + path
traefik.http.routers.api.rule=Host(`api.example.com`) && PathPrefix(`/api`)
```

**Parsing** (FR-014):
```go
var hostRegex = regexp.MustCompile(`Host\(['"]([^'"]+)['"]\)`)
var pathRegex = regexp.MustCompile(`Path(?:Prefix)?\(['"]([^'"]+)['"]\)`)

func parseTraefikRule(rule string) (hostnames []string, path string) {
    // Extract hostnames
    hostMatches := hostRegex.FindAllStringSubmatch(rule, -1)
    for _, match := range hostMatches {
        if len(match) > 1 {
            hostnames = append(hostnames, match[1])
        }
    }

    // Extract path
    pathMatch := pathRegex.FindStringSubmatch(rule)
    if len(pathMatch) > 1 {
        path = pathMatch[1]
    }

    return hostnames, path
}
```

---

### Service Configuration

#### Port

**Label**: `traefik.http.services.<name>.loadbalancer.server.port`
**Type**: Integer
**Priority**: 2 (Traefik labels)
**Description**: Backend service port.

**Example**:
```bash
traefik.http.services.web.loadbalancer.server.port=8080
```

#### Scheme

**Label**: `traefik.http.services.<name>.loadbalancer.server.scheme`
**Type**: Enum (`http`, `https`)
**Priority**: 2 (Traefik labels)
**Description**: Backend service protocol.

**Example**:
```bash
traefik.http.services.web.loadbalancer.server.scheme=https
```

---

### Service Name Linking

**Concept**: Traefik router references service by name.

**Example**:
```bash
# Router references service
traefik.http.routers.myapp.rule=Host(`app.example.com`)
traefik.http.routers.myapp.service=myapp

# Service definition
traefik.http.services.myapp.loadbalancer.server.port=8080
#                                        ^^^^^^ matches
```

**Implementation**:
```go
// Extract service name from router label
routerService := labels["traefik.http.routers.myapp.service"]  // "myapp"

// Find matching service labels
port := labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", routerService)]
```

---

## Label Priority Examples

### Example 1: DockTunnel Labels Win

```bash
docktunnel.web.hostname=app.example.com        # Priority 1: USED
traefik.http.routers.web.rule=Host(`other.com`) # Priority 2: IGNORED
```

**Result**: `app.example.com` (DockTunnel label takes precedence)

---

### Example 2: Traefik Labels Fallback

```bash
# No DockTunnel hostname label
traefik.http.routers.web.rule=Host(`app.example.com`)  # Priority 2: USED
```

**Result**: `app.example.com` (Traefik label used)

---

### Example 3: Auto-Detection Fallback

```bash
# No hostname in DockTunnel or Traefik labels
docktunnel.web.service=http://localhost:8080  # Priority 1: Partial config

# Auto-detection would attempt to find hostname, but fails validation
# Result: Configuration rejected (FR-004)
```

---

### Example 4: Port Auto-Detection

```bash
docktunnel.web.hostname=app.example.com  # Hostname specified
# No port specified

# Container exposes port 8080
# Auto-detection finds port 8080 (Priority 3)
```

**Result**: Service URL = `http://172.17.0.5:8080` (auto-detected)

---

### Example 5: Complete Configuration

```bash
# All labels specified (no auto-detection needed)
docktunnel.enable=true
docktunnel.web.hostname=app.example.com
docktunnel.web.service=http://172.17.0.5:8080
docktunnel.web.path=/api
docktunnel.web.originRequest.noTLSVerify=true
docktunnel.web.originRequest.connectTimeout=30s
docktunnel.web.originRequest.http2Origin=true
```

**Result**: Complete configuration from labels, no fallback needed

---

## Validation Rules

### Required Labels

```go
func validateRequiredLabels(labels map[string]string) error {
    // Enable flag
    if labels["docktunnel.enable"] != "true" {
        return errors.New("docktunnel.enable must be 'true'")
    }

    // At least one service configuration
    hasServiceConfig := false
    for key := range labels {
        if strings.HasPrefix(key, "docktunnel.") && strings.Contains(key, ".hostname") {
            hasServiceConfig = true
            break
        }
    }

    if !hasServiceConfig {
        return errors.New("at least one docktunnel.<name>.hostname label required")
    }

    return nil
}
```

### Hostname Uniqueness

```go
func validateHostnameUniqueness(
    containerLabels map[string]string,
    allHostnames map[string]string,  // hostname → containerID
) error {
    for key, hostname := range containerLabels {
        if !strings.HasSuffix(key, ".hostname") {
            continue
        }

        if existingContainerID, exists := allHostnames[hostname]; exists {
            return fmt.Errorf("hostname '%s' already used by container %s",
                hostname, existingContainerID)
        }
    }

    return nil
}
```

---

## Label Parsing Pseudocode

```
FUNCTION ParseContainerLabels(containerJSON):
  labels = containerJSON.Config.Labels

  // Filter by enable flag
  IF labels["docktunnel.enable"] != "true":
    RETURN EMPTY  // Skip container

  configs = MAP()

  FOR EACH label_key, label_value IN labels:
    IF NOT label_key.STARTS_WITH("docktunnel."):
      CONTINUE  // Not our label

    parts = label_key.SPLIT(".")
    IF parts.LENGTH < 3:
      CONTINUE  // Invalid format

    service_name = parts[1]  // e.g., "web", "api"
    attribute = parts[2]     // e.g., "hostname", "service"

    IF service_name NOT IN configs:
      configs[service_name] = NEW TunnelConfiguration(service_name)

    SWITCH attribute:
      CASE "hostname":
        configs[service_name].hostname = label_value
      CASE "service":
        configs[service_name].serviceURL = label_value
      CASE "port":
        configs[service_name].port = PARSE_INT(label_value)
      CASE "path":
        configs[service_name].path = label_value
      CASE "scheme":
        configs[service_name].protocol = label_value
      // ... (all originRequest.* attributes)
    END SWITCH
  END FOR

  // Apply 4-layer priority fallback
  FOR EACH config IN configs:
    ApplyPriorityFallback(config, containerJSON)
  END FOR

  RETURN configs
END FUNCTION
```

---

## References

- [Cloudflare Tunnel Ingress Configuration](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/tunnel-guide/remote/routing/)
- [Traefik Docker Provider Documentation](https://doc.traefik.io/traefik/providers/docker/)
- [Docker Label Documentation](https://docs.docker.com/config/labels-custom-metadata/)
