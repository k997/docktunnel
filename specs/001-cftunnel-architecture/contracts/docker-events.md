# Docker Events Contract

**Feature**: Cloudflare Tunnel Docker Controller
**Interface**: Docker Engine API (Events and Containers)
**Version**: Docker Engine API v1.45+

## Overview

This contract defines the interaction between DockTunnel and Docker daemon. The controller monitors container lifecycle events and inspects container metadata to configure tunnels.

---

## Event Stream Subscription

### Subscribe to Container Events

**Endpoint**:
```
GET /events
```

**Query Parameters**:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| type | string | No | Filter by event type (`container`) |
| event | string | No | Filter by event action (`start`, `die`, `destroy`) |

**Docker SDK Usage**:
```go
import "github.com/docker/docker/api/types/events"

ctx := context.Background()
eventsChan, errChan := cli.Events(ctx, events.ListOptions{
    Filters: map[string][]string{
        "type":   {"container"},
        "event":  {"start", "die", "destroy"},
    },
})

for {
    select {
    case event := <-eventsChan:
        handleContainerEvent(event)
    case err := <-errChan:
        logError("Docker event stream error", err)
    }
}
```

**Event Types Monitored** (FR-001):
- `start`: Container created or started
- `die`: Container stopped (exit code any)
- `destroy`: Container deleted from Docker

**Implementation Notes**:
- Event stream NEVER closes (reconnect on error)
- All events with `docktunnel.enable=true` labels processed (FR-001)
- Events filtered by Actor.Attributes before processing (FR-001)

---

## Event Message Format

### Container Start Event

```json
{
  "Type": "container",
  "Action": "start",
  "Actor": {
    "ID": "a1b2c3d4e5f6...",
    "Attributes": {
      "image": "nginx:latest",
      "name": "web-app",
      "docktunnel.enable": "true",
      "docktunnel.web.hostname": "app.example.com",
      "docktunnel.web.service": "http://localhost:8080"
    }
  },
  "time": 1704288000,
  "timeNano": 1704288000000000000
}
```

### Container Die Event

```json
{
  "Type": "container",
  "Action": "die",
  "Actor": {
    "ID": "a1b2c3d4e5f6...",
    "Attributes": {
      "exitCode": "0"
    }
  },
  "time": 1704288100,
  "timeNano": 1704288100000000000
}
```

### Container Destroy Event

```json
{
  "Type": "container",
  "Action": "destroy",
  "Actor": {
    "ID": "a1b2c3d4e5f6...",
    "Attributes": {}
  },
  "time": 1704288200,
  "timeNano": 1704288200000000000
}
```

---

## Container Inspection

### Get Container Details

**Endpoint**:
```
GET /containers/{id}/json
```

**Purpose**: Retrieve full container metadata for configuration parsing

**Docker SDK Usage**:
```go
import "github.com/docker/docker/api/types"

ctx := context.Background()
containerJSON, err := cli.ContainerInspect(ctx, containerID)
```

**Response** (200 OK):
```json
{
  "Id": "a1b2c3d4e5f6...",
  "Name": "/web-app",
  "State": {
    "Status": "running",
    "StartedAt": "2026-01-03T10:00:00Z"
  },
  "Config": {
    "Labels": {
      "docktunnel.enable": "true",
      "docktunnel.web.hostname": "app.example.com",
      "docktunnel.web.service": "http://localhost:8080",
      "traefik.http.routers.web.rule": "Host(`app.example.com`)",
      "traefik.http.services.web.loadbalancer.server.port": "8080"
    },
    "ExposedPorts": {
      "8080/tcp": {}
    }
  },
  "NetworkSettings": {
    "Networks": {
      "bridge": {
        "IPAMConfig": {},
        "Links": null,
        "Aliases": null,
        "NetworkID": "abc123...",
        "EndpointID": "def456...",
        "Gateway": "172.17.0.1",
        "IPAddress": "172.17.0.5",
        "IPPrefixLen": 16,
        "IPv6Gateway": "",
        "GlobalIPv6Address": "",
        "GlobalIPv6PrefixLen": 0,
        "MacAddress": "02:42:ac:11:00:05",
        "DriverOpts": null
      }
    }
  },
  "HostConfig": {
    "NetworkMode": "bridge"
  }
}
```

**Implementation Notes**:
- Called on every `start` event (FR-001)
- Labels parsed with 4-layer priority (FR-006 to FR-010)
- Network mode detected for IP address logic (FR-011)

---

## Label Parsing Contract

### DockTunnel Labels (Priority 1 - Highest)

**Format**: `docktunnel.<name>.<attribute>`

| Label | Type | Example | Required |
|-------|------|---------|----------|
| `docktunnel.enable` | boolean | `true` | Yes (gateway) |
| `docktunnel.<name>.hostname` | string | `app.example.com` | Yes |
| `docktunnel.<name>.service` | URL | `http://172.17.0.5:8080` | No |
| `docktunnel.<name>.path` | string | `/api` | No |
| `docktunnel.<name>.port` | int | `8080` | No |
| `docktunnel.<name>.scheme` | string | `https` | No |
| `docktunnel.<name>.originRequest.*` | varies | (see Cloudflare contract) | No |

**Parsing Rules** (FR-006):
```go
func parseDockTunnelLabels(labels map[string]string) map[string]TunnelConfiguration {
    configs := make(map[string]TunnelConfiguration)

    // Extract service names
    prefix := "docktunnel."
    for key, value := range labels {
        if !strings.HasPrefix(key, prefix) {
            continue
        }

        parts := strings.Split(strings.TrimPrefix(key, prefix), ".")
        if len(parts) < 2 {
            continue
        }

        serviceName := parts[0]
        attribute := parts[1]

        if _, exists := configs[serviceName]; !exists {
            configs[serviceName] = TunnelConfiguration{ServiceName: serviceName}
        }

        switch attribute {
        case "hostname":
            configs[serviceName].Hostname = value
        case "service":
            configs[serviceName].ServiceURL = value
        case "port":
            configs[serviceName].Port, _ = strconv.Atoi(value)
        // ... (all originRequest attributes)
        }
    }

    return configs
}
```

---

### Traefik Labels (Priority 2)

**Format**: `traefik.<sub-provider>.*`

| Label | Type | Example | Purpose |
|-------|------|---------|---------|
| `traefik.http.routers.<name>.rule` | string | `Host(\`app.com\`)` | Router rule |
| `traefik.http.services.<name>.loadbalancer.server.port` | int | `8080` | Service port |
| `traefik.http.services.<name>.loadbalancer.server.scheme` | string | `https` | Service protocol |

**Parsing Rules** (FR-007, FR-014):

**Hostname Extraction**:
```go
// Regex to extract Host() patterns
var hostRegex = regexp.MustCompile(`Host\(['"]([^'"]+)['"]\)`)

func extractHostnameFromTraefik(rule string) []string {
    matches := hostRegex.FindAllStringSubmatch(rule, -1)
    hostnames := make([]string, 0, len(matches))
    for _, match := range matches {
        if len(match) > 1 {
            hostnames = append(hostnames, match[1])
        }
    }
    return hostnames
}
```

**Port Extraction**:
```go
func extractPortFromTraefik(labels map[string]string, serviceName string) (int, bool) {
    key := fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", serviceName)
    if value, exists := labels[key]; exists {
        port, err := strconv.Atoi(value)
        return port, err == nil
    }
    return 0, false
}
```

**Service Name Linking**:
```go
// Router references Service via name
// traefik.http.routers.myapp.rule=Host(`app.com`)
// traefik.http.services.myapp.loadbalancer.server.port=8080
//                                      ^^^^^^ matches
```

---

### Auto-Detection (Priority 3)

**Network Mode Detection** (FR-011):

```go
func detectIPAddress(containerJSON *types.ContainerJSON) string {
    // Host networking
    if containerJSON.HostConfig.NetworkMode == "host" {
        return "localhost"  // FR-012
    }

    // Bridge networking
    for _, network := range containerJSON.NetworkSettings.Networks {
        if network.IPAddress != "" {
            return network.IPAddress  // FR-013
        }
    }

    return ""
}
```

**Port Auto-Detection** (FR-009):

```go
func detectExposedPort(containerJSON *types.ContainerJSON) int {
    // Use first exposed port
    for port := range containerJSON.Config.ExposedPorts {
        // Parse "8080/tcp" → 8080
        parts := strings.Split(string(port), "/")
        if portNum, err := strconv.Atoi(parts[0]); err == nil {
            return portNum
        }
    }
    return 0  // Not found, validation will fail
}
```

**Protocol Detection**:

```go
func detectScheme(labels map[string]string) string {
    // Check explicit scheme label
    if scheme, exists := labels["docktunnel.web.scheme"]; exists {
        return scheme
    }

    // Check Traefik scheme
    if scheme, exists := labels["traefik.http.services.web.loadbalancer.server.scheme"]; exists {
        return scheme
    }

    // Default to HTTP
    return "http"
}
```

---

## Event Processing Contract

### Event Flow

```
Docker Event → Event Channel → Debounce Window → Event Handler → State Update
                                                                       ↓
                                                               Cloudflare Sync
```

### Debouncing Logic (FR-024)

```go
type Debouncer struct {
    mu         sync.Mutex
    events     map[string]*time.Timer  // containerID → timer
    window     time.Duration           // 2 seconds
    handler    func(string)            // Event handler
}

func (d *Debouncer) Add(containerID string) {
    d.mu.Lock()
    defer d.mu.Unlock()

    // Cancel existing timer
    if timer, exists := d.events[containerID]; exists {
        timer.Stop()
    }

    // Create new timer
    d.events[containerID] = time.AfterFunc(d.window, func() {
        d.handler(containerID)
        delete(d.events, containerID)
    })
}
```

**Behavior**:
- Events within 2-second window are coalesced
- Only final state processed
- Reduces API calls during rapid container restarts

---

## Full Container Scan (Startup)

### List All Containers

**Endpoint**:
```
GET /containers/json
```

**Query Parameters**:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| all | bool | No | Include stopped containers (default: false) |

**Docker SDK Usage**:
```go
containers, err := cli.ContainerList(ctx, container.ListOptions{
    All: false,  // Only running containers
})

for _, container := range containers {
    // Inspect each container
    containerJSON, _ := cli.ContainerInspect(ctx, container.ID)

    // Parse labels
    if containerJSON.Config.Labels["docktunnel.enable"] == "true" {
        processContainer(containerJSON)
    }
}
```

**Implementation Notes** (FR-022):
- Called on controller startup
- Detects containers started while controller was down
- Reconciles with persisted state from shutdown

---

## Error Handling Contract

### Docker Daemon Connection Failures

**Scenario**: Docker daemon restarts while controller running

**Recovery Strategy**:
```go
func monitorDockerEvents(cli *client.Client) {
    for {
        ctx, cancel := context.WithCancel(context.Background())

        eventsChan, errChan := cli.Events(ctx, events.ListOptions{
            Filters: map[string][]string{
                "type":  {"container"},
                "event": {"start", "die", "destroy"},
            },
        })

        for {
            select {
            case event := <-eventsChan:
                handleEvent(event)
            case err := <-errChan:
                log.Error("Docker event stream error", err)
                cancel()
                time.Sleep(5 * time.Second)  // Backoff before reconnect
                goto RECONNECT  // Restart event loop
            }
        }
    RECONNECT:
    }
}
```

### Container Inspection Failures

**Scenario**: Container deleted before inspection completes

**Handling** (FR-039):
```go
containerJSON, err := cli.ContainerInspect(ctx, containerID)
if err != nil {
    if client.IsErrNotFound(err) {
        // Container deleted, skip event
        log.Warn("Container deleted before inspection", containerID)
        return
    }

    // Other errors (Docker daemon issue)
    log.Error("Container inspection failed", err)
    return  // Don't block other containers
}
```

---

## Socket Access Contract

### Docker Socket Connection

**Unix Socket Path**: `/var/run/docker.sock`

**Connection**:
```go
cli, err := client.NewClientWithOpts(
    client.FromEnv,  // Uses DOCKER_HOST env var
    client.WithAPIVersionNegotiation(),
)
```

**Environment Variables**:
```bash
DOCKER_HOST=unix:///var/run/docker.sock
DOCKER_API_VERSION=1.45
DOCKER_CERT_PATH=/path/to/cert  # Optional (for TLS)
DOCKER_TLS_VERIFY=1              # Optional (for TLS)
```

**Socket Mounting** (Container Deployment):
```yaml
# docker-compose.yml
version: '3'
services:
  docktunnel:
    image: docktunnel:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    #                                          ^^ Read-only recommended
```

**Permission Requirements** (Assumption #2):
- Controller process needs read access to Docker socket
- Typically run as root or docker group member
- Socket permissions: `srw-rw---- 1 root docker`

---

## Testing Contract

### Mock Interface

```go
type DockerClient interface {
    Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)
    ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
    ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
}
```

### Test Scenarios

1. **Container Start**: Event received, labels parsed, tunnel created
2. **Container Stop**: Event received, retention policy applied
3. **Container Delete**: Event received, entry removed from state
4. **No DockTunnel Labels**: Event received, skipped
5. **Rapid Restart**: Multiple events in 2s window, debounced to single update
6. **Docker Daemon Restart**: Stream breaks, reconnect succeeds
7. **Missing Exposed Ports**: Inspection succeeds, validation fails (FR-004)

---

## Performance Contract

### Event Processing SLA

| Metric | Target | Rationale |
|--------|--------|-----------|
| Event to Handler | < 100ms | Minimize latency |
| Handler to State Update | < 500ms | Prevent bottlenecks |
| State Update to Cloudflare API | < 5s | FR-002 compliance |

**Burst Handling** (SC-002):
- 100 containers starting simultaneously
- Debouncer coalesces rapid events
- Worker pool (5 goroutines) processes in parallel

### Resource Usage

| Metric | Target | Rationale |
|--------|--------|-----------|
| Memory (100 containers) | < 50MB | In-memory state maps |
| Goroutines | ~20 | Event listener + workers + sync loop |
| Socket Connections | 1 | Reused Docker client |

---

## Security Contract

### Label Injection Prevention

**Scenario**: Malicious container labels attempt command injection

**Validation**:
```go
func sanitizeLabelValue(key, value string) error {
    // Reject shell metacharacters
    if strings.ContainsAny(value, "$`';|&()<>") {
        return fmt.Errorf("invalid characters in label %s", key)
    }

    // Limit length
    if len(value) > 1024 {
        return fmt.Errorf("label value too long: %s", key)
    }

    return nil
}
```

**Constitution Alignment**: Quality Standards (Security) - prevent label injection attacks

---

## References

- [Docker Engine API Documentation](https://docs.docker.com/engine/api/)
- [Docker SDK for Go](https://github.com/docker/docker-ce/blob/main/components/cli/cli/command/formatter/labels.go)
- [Docker Events Reference](https://docs.docker.com/engine/reference/commandline/events/)
