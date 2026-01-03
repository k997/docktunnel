# Developer Quickstart Guide

**Feature**: Cloudflare Tunnel Docker Controller
**Target Audience**: Developers contributing to DockTunnel
**Prerequisites**: Go 1.24+, Docker, Cloudflare account

---

## Table of Contents

1. [Development Environment Setup](#development-environment-setup)
2. [Building the Project](#building-the-project)
3. [Running Tests](#running-tests)
4. [Local Development](#local-development)
5. [Code Organization](#code-organization)
6. [Common Tasks](#common-tasks)
7. [Debugging Tips](#debugging-tips)

---

## Development Environment Setup

### Prerequisites

Install required tools:

```bash
# Go 1.24+
go version

# Docker
docker --version

# Cloudflare account with API token
# Sign up at: https://dash.cloudflare.com/sign-up
```

### Clone Repository

```bash
git clone https://github.com/yourusername/docktunnel.git
cd docktunnel
```

### Install Dependencies

```bash
go mod download
```

### Verify Installation

```bash
go build -o docktunnel ./cmd/docktunnel
./docktunnel --version
```

---

## Building the Project

### Standard Build

```bash
go build -o docktunnel ./cmd/docktunnel
```

### Build with Version Info

```bash
VERSION=$(git describe --tags --always)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT=$(git rev-parse HEAD)

go build \
  -ldflags "-X main.Version=${VERSION} \
            -X main.BuildDate=${BUILD_TIME} \
            -X main.GitCommit=${GIT_COMMIT}" \
  -o docktunnel \
  ./cmd/docktunnel
```

### Cross-Platform Build

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o dist/docktunnel-linux-amd64 ./cmd/docktunnel

# macOS
GOOS=darwin GOARCH=amd64 go build -o dist/docktunnel-darwin-amd64 ./cmd/docktunnel

# Windows
GOOS=windows GOARCH=amd64 go build -o dist/docktunnel-windows-amd64.exe ./cmd/docktunnel
```

### Using Make

```bash
make build        # Standard build
make fmt          # Format code
make deps         # Download dependencies
make clean        # Clean build artifacts
```

---

## Running Tests

### Run All Tests

```bash
go test ./...
```

### Run with Coverage

```bash
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Run Specific Package Tests

```bash
# Controller tests
go test ./internal/controller/...

# Label parser tests
go test ./internal/controller/ -run TestLabelParser

# Validator tests
go test ./internal/controller/ -run TestValidator
```

### Run with Verbose Output

```bash
go test -v ./internal/controller/...
```

### Run Integration Tests

```bash
# Requires running Docker daemon
go test ./tests/integration/...
```

### Using Make

```bash
make test         # Run all tests
make test-coverage # Run with coverage
```

---

## Local Development

### 1. Create Configuration File

Create `config.yaml`:

```yaml
log:
  level: debug       # Verbose logging for development
  format: text       # Human-readable output

cloudflare:
  accountId: "your-account-id"
  apiToken: "your-api-token"
  tunnelName: "DockTunnel-Dev"
  rateLimit: 10
  maxRetries: 3

controller:
  flappingWindow: 60s
  flappingThreshold: 5
  coolingPeriod: 300s
  debounceDuration: 2s

cleanup:
  onExit: false     # Don't clean up during development
```

### 2. Start Controller

```bash
./docktunnel --config config.yaml
```

### 3. Launch Test Container

```bash
docker run -d \
  --name=test-web \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=test-dev.example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  nginx:latest
```

### 4. Verify Logs

Controller should log:

```
INFO    Container started    container_id=a1b2c3d4... container_name=/test-web
INFO    Parsed configuration    service=web hostname=test-dev.example.com
INFO    Created tunnel route    hostname=test-dev.example.com service=http://172.17.0.2:8080
INFO    Cloudflare config updated    ingress_count=2
```

### 5. Test Accessibility

```bash
curl https://test-dev.example.com
# Should return NGINX welcome page
```

---

## Code Organization

### Directory Structure

```
cmd/docktunnel/              # Application entry point
└── main.go                  # Initialization, signal handling

internal/                    # Private application code
├── config/                  # Configuration management
│   ├── config.go           # Viper-based config loading
│   └── config_test.go
├── docker/                  # Docker integration
│   ├── monitor.go          # Event stream monitoring
│   ├── client.go           # Docker API client wrapper
│   └── monitor_test.go
├── cloudflareManager/      # Cloudflare integration
│   ├── tunnel.go           # Tunnel ingress management
│   ├── rate_limiter.go     # Token bucket rate limiting
│   └── retry.go            # Exponential backoff
├── controller/             # Core business logic
│   ├── controller.go       # Event orchestration
│   ├── label_parser.go     # 4-layer configuration resolution
│   └── validator.go        # Hostname uniqueness, validation
├── state/                  # State management
│   ├── manager.go          # State maps, reconciliation
│   └── persistence.go      # State save/load
├── events/                 # Event structures
│   └── event.go
└── logger/                 # Logging utilities
    └── logger.go

pkg/types/                  # Public types
└── tunnel.go               # TunnelEntry, IngressRule, etc.

tests/                      # Test code
├── integration/            # Integration tests
│   ├── docker_events_test.go
│   └── end_to_end_test.go
├── mocks/                  # Mock implementations
│   ├── cloudflare_api.go
│   └── docker_client.go
└── testhelpers/            # Test utilities
    └── container.go
```

---

## Common Tasks

### Add New Label Attribute

**Example**: Adding `docktunnel.<name>.customAttribute`

1. **Update Data Model** (`pkg/types/tunnel.go`):
```go
type TunnelConfiguration struct {
    // ... existing fields
    CustomAttribute string `json:"customAttribute,omitempty"`
}
```

2. **Update Label Parser** (`internal/controller/label_parser.go`):
```go
func parseLabels(labels map[string]string) (*TunnelConfiguration, error) {
    // ... existing parsing

    if attr, exists := labels["docktunnel.web.customAttribute"]; exists {
        config.CustomAttribute = attr
    }
}
```

3. **Add Tests** (`internal/controller/label_parser_test.go`):
```go
func TestParseCustomAttribute(t *testing.T) {
    labels := map[string]string{
        "docktunnel.enable": "true",
        "docktunnel.web.hostname": "app.example.com",
        "docktunnel.web.customAttribute": "custom-value",
    }

    config, err := ParseLabels(labels)
    assert.NoError(t, err)
    assert.Equal(t, "custom-value", config.CustomAttribute)
}
```

---

### Add New Origin Request Setting

**Example**: Adding `customTimeout` to OriginRequest

1. **Update Type** (`pkg/types/tunnel.go`):
```go
type OriginRequestConfig struct {
    // ... existing fields
    CustomTimeout time.Duration `json:"customTimeout,omitempty"`
}
```

2. **Update Parser** (`internal/controller/label_parser.go`):
```go
func parseOriginRequest(labels map[string]string) (*OriginRequestConfig, error) {
    config := &OriginRequestConfig{}

    // Parse customTimeout
    if timeout, exists := labels["docktunnel.web.originRequest.customTimeout"]; exists {
        duration, err := time.ParseDuration(timeout)
        if err != nil {
            return nil, fmt.Errorf("invalid customTimeout: %w", err)
        }
        config.CustomTimeout = duration
    }

    // ... rest of parsing
    return config, nil
}
```

---

### Implement New Retention Policy Type

**Example**: Adding `scheduled` retention (delete at specific time)

1. **Update Enum** (`pkg/types/tunnel.go`):
```go
type PolicyType int
const (
    Immediate PolicyType = iota
    Timed
    Forever
    Scheduled  // New type
)

type RetentionPolicy struct {
    Type      PolicyType
    Duration  time.Duration  // For Timed
    Schedule  time.Time      // For Scheduled (new)
}
```

2. **Update GC Logic** (`internal/state/manager.go`):
```go
func (sm *StateManager) RunGC() {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    now := time.Now()

    for containerID, entry := range sm.pendingDeletes {
        switch entry.RetentionPolicy.Type {
        case Immediate:
            delete(sm.pendingDeletes, containerID)

        case Timed:
            if now.Sub(*entry.DeletedAt) > entry.RetentionPolicy.Duration {
                delete(sm.pendingDeletes, containerID)
            }

        case Scheduled:
            if now.After(entry.RetentionPolicy.Schedule) {
                delete(sm.pendingDeletes, containerID)
            }

        case Forever:
            // Never delete
        }
    }
}
```

---

## Debugging Tips

### Enable Debug Logging

```yaml
log:
  level: debug
```

Or via environment variable:

```bash
DOCKTUNNEL_LOG_LEVEL=debug ./docktunnel
```

### Inspect Container Labels

```bash
docker inspect test-web --format='{{json .Config.Labels}}' | jq
```

### View Current Cloudflare Configuration

```bash
# Cloudflare CLI (cloudflared)
cloudflared tunnel ingress list <tunnel-id>
```

### Check State File

```bash
# State file location (default)
./docktunnel --state-file /tmp/docktunnel-state.json

# View state
cat /tmp/docktunnel-state.json | jq
```

### Trace Event Processing

Add debug logging in event handler:

```go
func (c *Controller) HandleEvent(event events.Message) {
    log.Debug("Processing event",
        "container_id", event.Actor.ID,
        "action", event.Action,
        "labels", event.Actor.Attributes)

    // ... event processing
}
```

### Mock Cloudflare API for Testing

Use mock in tests:

```go
func TestControllerWithMock(t *testing.T) {
    mockClient := &mocks.MockCloudflareClient{
        Tunnels: make(map[string]*Tunnel),
        ShouldFail: false,
    }

    controller := NewController(mockClient, mockDockerClient)
    // ... test logic
}
```

---

## Performance Profiling

### CPU Profiling

```bash
go build -o docktunnel ./cmd/docktunnel
./docktunnel --cpuprofile=cpu.prof

# Analyze
go tool pprof cpu.prof
```

### Memory Profiling

```bash
./docktunnel --memprofile=mem.prof

# Analyze
go tool pprof mem.prof
```

### HTTP Profiling (pprof)

Add to code:

```go
import _ "net/http/pprof"

func main() {
    go func() {
        log.Println(http.ListenAndServe("localhost:6060", nil))
    }()

    // ... rest of application
}
```

Access at: `http://localhost:6060/debug/pprof/`

---

## Common Issues

### Issue: "Permission denied" accessing Docker socket

**Solution**:
```bash
# Add user to docker group
sudo usermod -aG docker $USER

# Re-login or run with sudo
sudo ./docktunnel
```

---

### Issue: "Invalid API token" from Cloudflare

**Solution**:
- Verify token has required permissions (Account: R/W, Zone: R/W, Tunnel: R/W)
- Regenerate token at: https://dash.cloudflare.com/profile/api-tokens

---

### Issue: Container not processed

**Debugging**:
```bash
# Check labels
docker inspect <container> --format='{{json .Config.Labels}}' | jq

# Verify docktunnel.enable=true
docker inspect <container> --format='{{index .Config.Labels "docktunnel.enable"}}'
```

---

### Issue: Tests fail with "connection refused"

**Solution**:
- Ensure Docker daemon is running: `docker ps`
- Check Docker socket: `ls -l /var/run/docker.sock`

---

## Contributing Workflow

### 1. Create Feature Branch

```bash
git checkout -b feature/your-feature-name
```

### 2. Make Changes

```bash
# Edit code
vim internal/controller/label_parser.go

# Run tests
go test ./internal/controller/...

# Format code
gofmt -w internal/controller/label_parser.go
```

### 3. Commit Changes

```bash
git add internal/controller/label_parser.go
git commit -m "feat(parser): add support for custom label attribute"
```

### 4. Run Full Test Suite

```bash
make test
```

### 5. Push and Create PR

```bash
git push origin feature/your-feature-name
# Create PR on GitHub
```

---

## Additional Resources

- [Go Documentation](https://golang.org/doc/)
- [Docker SDK for Go](https://github.com/docker/docker-ce/blob/main/components/cli/cli/command/formatter/labels.go)
- [Cloudflare API Documentation](https://developers.cloudflare.com/api/)
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
