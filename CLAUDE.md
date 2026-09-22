# DockTunnel - CLAUDE.md

## Project Overview

**DockTunnel** is an automated tool that manages the connection between Docker containers and Cloudflare Tunnel. It monitors Docker container events and automatically configures Cloudflare Tunnel and DNS records, enabling containerized services to be accessible on the internet through custom domains.

### Key Purpose
- Automatically expose Docker containers via Cloudflare Tunnel using container labels
- Eliminate manual configuration of Cloudflare tunnels and DNS records
- Provide dynamic service discovery and tunnel management
- Ensure secure and reliable access to containerized services

## Architecture Overview

### High-Level Architecture
DockTunnel follows an **event-driven architecture** with the following components:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Docker        │    │   Controller    │    │  Cloudflare     │
│   Events        │    │   & Logic       │    │   Manager       │
│   Listener      │◄──►│                 │◄──►│                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         │              ┌─────────────────┐              │
         │              │     Config     │              │
         │              │     Manager    │              │
         │              └─────────────────┘              │
         │                       │                       │
         └───────────────────────────────────────────────┘
```

### Core Components

#### 1. Main Package (`cmd/docktunnel/main.go`)
- **Entry point** of the application
- **Initialization sequence**: Config → Logger → Docker Manager → Cloudflare Manager → Controller
- **Graceful shutdown** with signal handling and resource cleanup
- **Event-driven processing** with goroutines for event listening and processing

#### 2. Docker Manager (`internal/docker/monitor.go`)
- **Docker client** wrapper for Docker operations
- **Event listening**: Monitors Docker daemon events (start/stop/die)
- **Container scanning**: Discovers running containers with docktunnel labels
- **Event filtering**: Only processes containers with `docktunnel.enable=true` labels

#### 3. Cloudflare Manager (`internal/cloudflareManager/tunnel.go`)
- **Cloudflare API** client wrapper
- **Tunnel management**: Get/create tunnels with fallback by name or ID
- **Configuration updates**: Manages Cloudflare Tunnel ingress rules
- **DNS record management**: Batch operations for creating/updating/deleting DNS records
- **Rate limiting and retry logic**: Implements token bucket and exponential backoff
- **Error handling**: Intelligent retry mechanism for transient failures

#### 4. Controller (`internal/controller/controller.go`)
- **Core business logic** orchestrating Docker and Cloudflare operations
- **Event dispatching**: Handles container lifecycle events
- **Rule validation**: Ensures hostname uniqueness and required fields
- **State management**: Maintains internal rule mappings and container health
- **Container flapping detection**: Prevents rapid state changes with intelligent backoff
- **Debouncing**: Groups rapid changes to reduce API calls
- **Synchronization**: Ensures Cloudflare state matches Docker container state

#### 5. Configuration Management (`internal/config/config.go`)
- **Viper-based** configuration with YAML, environment variable, and default value support
- **Multi-source configuration**: Files, environment variables, defaults
- **Structured configuration**: Log, Cloudflare, Controller, and Cleanup sections
- **Validation**: Ensures required parameters are provided

#### 6. Label Parser (`internal/label/`)
- **Label-based configuration** parsing (`parser.go` + `builder.go`)
- **Flexible label schema**: `docktunnel.<service-name>.<attribute>`
- **Comprehensive attribute support**: hostname, service, port, path, originRequest settings
- **Network-aware**: Auto-detects container IPs and handles host network mode;
  `docktunnel.<svc>.network` / `traefik.docker.network` select the Docker
  network used for the IP (`host` → `localhost`)
- **Protocol detection**: Supports HTTP/HTTPS service URL generation
- **Traefik compatibility**: Optional minimal subset, opt-in via
  `docktunnel.traefik.enable=true`; unsafe/unsupported rules are rejected with
  a WARN (see Container Label System below)

#### 7. Validators (`internal/controller/validator.go`)
- **Rule validation pipeline**: Composite pattern with multiple validators
- **Hostname uniqueness**: Prevents duplicate hostname conflicts
- **Service name uniqueness**: Ensures service name uniqueness
- **Required fields**: Validates mandatory configuration

#### 8. Events Package (`internal/events/event.go`)
- **Unified event structure**: Standardized data transfer between components
- **Docker SDK integration**: Uses native Docker types for compatibility

## Technology Stack

### Core Dependencies
- **Go 1.24**: Primary programming language
- **Docker SDK**: `github.com/docker/docker/v28` - Docker daemon communication
- **Cloudflare SDK**: `github.com/cloudflare/cloudflare-go/v5` - Cloudflare API interaction
- **Viper**: `github.com/spf13/viper` - Configuration management
- **Standard library**: `log/slog` for logging, `context` for cancellation

### Key Libraries
- **Rate limiting**: `golang.org/x/time/rate` - Token bucket algorithm
- **Math**: `math/rand` - Random jitter for backoff
- **String operations**: Standard Go string manipulation
- **Time handling**: `time` package for durations and timestamps

## Design Patterns & Conventions

### 1. Clean Architecture
- **Layered separation**: `cmd/` → `internal/` → domain packages
- **Dependency injection**: Managers and controllers receive dependencies
- **Interface segregation**: Clear boundaries between components

### 2. Event-Driven Design
- **Producer-consumer pattern**: Docker events → processing pipeline
- **Async processing**: Goroutines for non-blocking operations
- **Channel-based communication**: Safe data exchange between goroutines

### 3. Configuration Management
- **Hierarchical configuration**: Defaults → Files → Environment variables
- **Environment variable mapping**: Automatic dot-to-underscore conversion
- **Type-safe configuration**: Structured configuration with validation

### 4. Error Handling
- **Comprehensive error propagation**: Errors wrapped with context
- **Retry mechanisms**: Exponential backoff with jitter for API calls
- **Graceful degradation**: Continue operation on non-critical failures

### 5. State Management
- **Memory-based caching**: In-memory maps for performance
- **Mutex protection**: Thread-safe state access
- **Atomic updates**: Lock-based synchronization for critical sections

### 6. Container Health Monitoring
- **Flapping detection**: Container restart pattern analysis
- **Cooling periods**: Exponential backoff for unstable containers
- **Debouncing**: Rapid event grouping to prevent API spam

## Build, Test, and Development

### Build Commands
```bash
# Build the application
make build          # Format code and build binary
make fmt            # Format Go code
make fmt-check      # Check code formatting
make deps           # Download dependencies
make test           # Run tests
make test-coverage  # Run tests with coverage
make clean          # Clean build artifacts
make run            # Build and run the application

# Docker operations
make docker-build               # Build Docker image
make docker-build-latest        # Build Docker image with latest tag
```

### Manual Build Commands
```bash
# Build from source
go build -o docktunnel ./cmd/docktunnel

# Build with specific flags
go build -ldflags "-X main.Version=dev -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.GitCommit=$(git rev-parse HEAD)" -o docktunnel ./cmd/docktunnel

# Run tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Format code
gofmt -s -w .
```

### Configuration
Default configuration locations (in order of precedence):
1. `CONFIG_PATH` env var — full path to the config file (e.g. systemd
   deployments); when set, it overrides the search paths below
2. `./config.yaml` (current directory)
3. `/etc/docktunnel/config.yaml`
4. Environment variables (e.g., `DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID`)

### Docker Usage
```bash
# Run as Docker container
docker run -d \
  --name=docktunnel \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ./config.yaml:/etc/docktunnel/config.yaml \
  docktunnel:latest

# With custom configuration
docker run -d \
  --name=docktunnel \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID="your-account-id" \
  -e DOCKTUNNEL_CLOUDFLARE_API_TOKEN="your-api-token" \
  docktunnel:latest
```

## Container Label System

### Label Schema
```yaml
docktunnel.enable=true
docktunnel.<service-name>.hostname=example.com
docktunnel.<service-name>.service=http://localhost:8080
docktunnel.<service-name>.path=/api
docktunnel.<service-name>.originRequest.connectTimeout=30s
docktunnel.<service-name>.originRequest.noTLSVerify=true
```

### Supported Labels
- **Core configuration**: `hostname`, `service`, `port`, `path`
- **Retention**: `docktunnel.<svc>.delete_retention` (primary spelling;
  `docktunnel.<svc>.retention` legacy alias) and the global
  `docktunnel.delete_retention` / `docktunnel.retention`. Values:
  `immediate`/`0`, `forever`/`keep`, or a duration like `30m`/`1h`/`7d`.
  Default is `immediate`.
- **Access (Zero Trust)**: `docktunnel.<svc>.access.required` /
  `access.team_name` / `access.aud_tag` (aliases: `originRequest.access.*`
  prefix and camelCase `teamName` / `audTag`).
- **Network selection**: `docktunnel.<svc>.network` (Docker network used for
  the container IP; `host` → `localhost`) and `traefik.docker.network`.
- **Traefik opt-in**: `docktunnel.traefik.enable=true` is REQUIRED before any
  `traefik.*` label is parsed. HTTP rules may only contain `Host(...)` /
  `Path(...)` clauses; TCP rules only `HostSNI(...)`. Unsupported labels fall
  into three categories:
  (a) **rejected with a WARN** (cannot be mapped safely; the router is skipped
  and never exposed): `!`, `&&`, `||`, `HostRegexp`, `PathPrefix`,
  `PathRegexp`, `Method`, `Header`, `Query`, `ClientIP`, `middlewares`
  (refusing to expose auth-protected routes without auth), and `weighted`
  services or routers pointing at a service without a usable
  `loadbalancer.server` (would otherwise silently degrade to a port-0 route);
  (b) **benignly ignored** (no security impact, Info log only): `entryPoints`,
  `priority`, `tls`, UDP;
  (c) **supported**: `Host(...)`/`Path(...)`, `server.port`/`server.scheme`/
  `server.url`, `HostSNI(...)`.
  Path-only (no `Host`) HTTP routers, and unquoted `Host(a.com)` styles that
  yield no hostname, are skipped with a WARN. `server.url` takes priority over
  `server.port`/`server.scheme`.
- **Network settings**: `proto`, automatic IP detection
- **Origin request settings**: 20+ configuration options for TLS, timeouts, proxy settings
- **Container detection**: `docktunnel.enable` (required)

### Container Example
```bash
docker run -d \
  --name=web-app \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=app.example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  nginx:latest
```

## Key Features

### 1. Automatic Tunnel Management
- **Dynamic tunnel creation**: Creates tunnels if none exist
- **Tunnel discovery**: Finds existing tunnels by ID or name
- **Secret generation**: Auto-generates tunnel secrets

### 2. Intelligent Container Management
- **Event-based synchronization**: Real-time response to container changes
- **Flapping detection**: Prevents configuration thrashing
- **Debouncing**: Groups rapid changes to reduce API calls

### 3. DNS Management
- **Batch operations**: Efficient bulk DNS record management
- **Zone-aware**: Automatic zone ID resolution
- **Cache optimization**: Reduces API calls with intelligent caching

### 4. Fault Tolerance
- **Retry mechanisms**: Exponential backoff for API failures
- **Rate limiting**: Token bucket algorithm to prevent API overload
- **Graceful shutdown**: Clean resource cleanup on exit

### 5. Configuration Flexibility
- **Multiple sources**: YAML files, environment variables, defaults
- **Comprehensive options**: 20+ origin request configuration options
- **Validation**: Ensures configuration correctness

## Important Implementation Details

### 1. Container IP Detection
- **Host network mode**: Uses `localhost` when containers use host networking
- **Bridge networks**: Auto-detects bridge network IP addresses
- **Fallback**: Uses first available network IP

### 2. Cloudflare API Integration
- **Rate limiting**: Configurable requests per second (default: 4, matching
  Cloudflare's official ~1200 req/5min limit; higher values risk 429s)
- **Retry logic**: Configurable max retries (default: 3) with exponential backoff
- **Error categorization**: Retries rate limits, server errors, and timeouts

### 3. State Management
- **In-memory caching**: Rules and container health stored in memory
- **Synchronization**: Atomic updates with mutex protection
- **Cleanup**: Optional resource cleanup on exit

### 4. Logging
- **Structured logging**: Uses Go's `log/slog` for consistent logging
- **Configurable levels**: debug, info, warn, error
- **Multiple formats**: Text or JSON output

## Development Guidelines

### Code Organization
- **Follow Go conventions**: Standard Go project layout
- **Internal packages**: Keep private implementation in `internal/`
- **Clear interfaces**: Define interfaces for external dependencies
- **Error handling**: Wrap errors with context for debugging

### Testing
- **Unit tests**: Place test files alongside source files (`*_test.go`)
- **Mock dependencies**: Use interfaces for testability
- **Integration tests**: Test Docker and Cloudflare interactions carefully

### Configuration Management
- **Environment support**: Map configuration to environment variables
- **Defaults**: Provide sensible defaults for all options
- **Validation**: Validate configuration at startup

### Performance Considerations
- **Batch operations**: Use batch API calls for DNS operations
- **Caching**: Cache zone IDs and reduce API calls
- **Rate limiting**: Respect Cloudflare API rate limits

## Deployment

### Prerequisites
- Docker 18.09+ (for container usage)
- Go 1.24+ (for development)
- Cloudflare account and API token with:
  - Account: Read/Write
  - Zone: Read/Write
  - Tunnel: Read/Write

### Production Deployment
```bash
# Systemd service
sudo systemctl enable docktunnel
sudo systemctl start docktunnel

# Docker container (recommended)
docker run -d \
  --name=docktunnel \
  --restart=unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /etc/docktunnel/config.yaml:/etc/docktunnel/config.yaml \
  docktunnel:latest
```

### Configuration File Example
```yaml
log:
  level: info
  format: text

cloudflare:
  accountId: "your-account-id"
  apiToken: "your-api-token"
  tunnelName: "DockTunnel"
  catchAll: "http_status:404"
  rateLimit: 4
  maxRetries: 3
  retryDelay: 1s
  maxRetryDelay: 30s

controller:
  flappingWindow: 60s
  flappingThreshold: 5
  coolingPeriod: 300s
  maxCoolingPeriod: 1800s
  debounceDuration: 2s

cleanup:
  onExit: false   # 总开关，默认 false（false 时退出绝不清理）
  # strategy: graceful-cleanup | fast-exit（仅 onExit=true 时生效）
```

## Known Limitations

### Current Constraints
- **Single tunnel support**: Currently manages one Cloudflare tunnel per instance
- **Host network limitation**: IP detection limited to bridge and host networks
  (selectable via `docktunnel.<svc>.network` / `traefik.docker.network`)
- **Traefik compatibility**: Only the documented minimal subset is supported
  (see Container Label System); unsafe labels are rejected with a WARN and
  benign unsupported fields are ignored with an Info log — nothing is silently
  exposed
- **Label validation**: Limited validation of service URLs and hostnames

### Future Enhancements
- Multi-tunnel support
- Advanced container networking support
- Configuration drift detection
- Metrics and monitoring integration

## Troubleshooting

### Common Issues
1. **Docker connection**: Verify `/var/run/docker.sock` is accessible
2. **Cloudflare API**: Check account ID, API token, and permissions
3. **Container labels**: Ensure `docktunnel.enable=true` is set
4. **Port conflicts**: Verify service URLs are accessible from container network

### Debug Mode
```bash
# Enable debug logging
export DOCKTUNNEL_LOG_LEVEL=debug
./docktunnel

# With verbose output
go build -ldflags "-X main.Version=debug" -o docktunnel ./cmd/docktunnel
./docktunnel
```

This CLAUDE.md file provides comprehensive context for future Claude Code instances working on the DockTunnel project, covering architecture, development patterns, configuration, and operational guidance.

## Recent Changes
- 001-cftunnel-architecture: Added Go 1.24
- 002-release-engineering: Aligned docs/config/CI/Docker with fixed behavior —
  Traefik labels are opt-in (`docktunnel.traefik.enable=true`) with a minimal
  safe subset (Host/Path/HostSNI only, middlewares & unsafe matchers rejected);
  cleanup is gated by `cleanup.onExit` (default false) with
  `graceful-cleanup`/`fast-exit` strategies; `cloudflare.rateLimit` default is
  now 4 (Cloudflare official ~1200 req/5min); `CONFIG_PATH` selects the config
  file path; state persistence is implemented (no longer a limitation); image
  builds are multi-arch (amd64/arm64) to GHCR + optional Docker Hub; CI runs
  govulncheck; Dockerfile pins golang:1.24.6-alpine with GOTOOLCHAIN=local and
  a HEALTHCHECK on /healthz.
