# DockTunnel

English | [简体中文](README.zh-CN.md)

[![Go Report Card](https://goreportcard.com/badge/k997/docktunnel)](https://goreportcard.com/report/k997/docktunnel)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Introduction

**DockTunnel** is a smart Cloudflare Tunnel Docker controller that automatically manages Cloudflare Tunnel configuration and DNS records by listening to Docker container events. It bridges the gap between Docker's dynamic environment and Cloudflare Tunnel's static configuration, making it easy to expose containerized services to the internet through custom domains.

### Key Features

- **Event-driven architecture**: Listens to container start/stop events in real time and syncs configuration automatically
- **Smart label parsing**: Multi-level configuration precedence (custom labels → Traefik-compatible → auto-detection → global defaults)
- **Traefik compatibility**: An optional minimal compatible subset that must be enabled explicitly with `docktunnel.traefik.enable=true` (see [Traefik-Compatible Labels](#traefik-compatible-labels))
- **Advanced networking**: Automatic container IP detection with Bridge/Host network mode support
- **Robust fault tolerance**:
  - Flapping detection
  - Smart backoff and cooldown management
  - Event debouncing
  - Three-layer fault tolerance design (config validation, state sync, resource cleanup)
- **Flexible cleanup policies**: Immediate delete, delayed retention, permanent retention, and more
- **Batch DNS management**: Efficient batched DNS record operations that reduce API calls
- **Rate limiting & retries**: Token bucket + exponential backoff to protect API resources

## How It Works

DockTunnel follows an **event-driven + state reconciliation** architecture:

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Docker        │    │   Controller    │    │  Cloudflare     │
│   Events        │    │   & Logic       │    │   Manager       │
│   Listener      │◄──►│                 │◄──►│                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         │              ┌─────────────────┐              │
         │              │     Config      │              │
         │              │     Manager     │              │
         │              └─────────────────┘              │
         │                       │                       │
         └───────────────────────────────────────────────┘
```

### Data Flow

1. **Event source**: The Docker daemon emits `START`, `DIE`, and `DESTROY` events
2. **Event ingestion**: The watcher module receives events and filters out containers without the `docktunnel.enable=true` label
3. **Config processing**:
   - **Inspector**: Queries the Docker API for container details (labels, ports, networks)
   - **Parser**: Applies the 4-level precedence strategy to produce normalized `IngressRule` objects
   - **Policy engine**: Handles `retention` policies and GC logic
4. **State management**: Maintains a desired state snapshot
5. **Sync execution**: The syncer module computes the diff and updates the remote configuration through the Cloudflare API

## Quick Start

### Requirements

- **Docker**: 18.09+ (for container deployment)
- **Go**: 1.24+ (development only, see [go.mod](go.mod))
- A **Cloudflare account** and API token with the following permissions:
  - Account: Read/Write
  - Zone: Read/Write
  - Tunnel: Read/Write

### Installation

#### Option 1: Docker (recommended)

```bash
docker run -d \
  --name=docktunnel \
  --restart=unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v ./config.yaml:/etc/docktunnel/config.yaml:ro \
  -v docktunnel-state:/var/lib/docktunnel \
  ghcr.io/k997/docktunnel:latest
```

#### Option 2: Build from source

```bash
git clone https://github.com/k997/docktunnel.git
cd docktunnel

# Build the binary
go build -o docktunnel ./cmd/docktunnel

# Run
./docktunnel
```

#### Option 3: Make

```bash
# Download dependencies
make deps

# Build and run
make build
make run
```

### Running the cloudflared connector (required)

> **Important**: DockTunnel **only manages tunnel configuration and DNS records**
> (writing ingress rules via the Cloudflare API); it does **not carry traffic
> itself**. For your domains to actually work, you must run a **cloudflared
> connector** (the `cloudflared tunnel run` daemon of Cloudflare Tunnel) on a
> machine that **can reach the container network**, and register it in
> Cloudflare against the same Tunnel ID that DockTunnel manages.

- For installing and connecting cloudflared, see the official Cloudflare docs:
  - Install: <https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/>
  - Create and run a tunnel: <https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/get-started/create-remote-tunnel/>
- **Reachability prerequisite**: By default DockTunnel uses the container's
  **bridge network IP** (e.g. `172.17.0.2`) as the ingress origin address. That
  IP is only reachable from processes **on the same Docker network** (or the
  same host), so the cloudflared connector must run on a **machine that can
  ping the container IP** — the common approach is to run the connector as a
  container on the same Docker network as your workloads, or to specify the
  right network with the `docktunnel.<svc>.network` /
  `traefik.docker.network` labels (`host` means `localhost`).
- Without a connector, the DNS and tunnel configuration are still correct, but
  the domains will time out or error.

### Configuration

DockTunnel supports several configuration sources, in descending precedence:

1. Environment variables (prefix `DOCKTUNNEL_`)
2. Config file `./config.yaml`, `/etc/docktunnel/config.yaml`, or the full path
   given by the `CONFIG_PATH` environment variable (common for systemd deployments)
3. Built-in defaults

#### Config file example

Create a `config.yaml`:

```yaml
log:
  level: info          # Log level: debug, info, warn, error
  format: text         # Log format: text or json

cloudflare:
  accountId: "your-cloudflare-account-id"
  apiToken: "your-cloudflare-api-token"
  tunnelName: "DockTunnel"      # Tunnel name; created automatically if empty
  tunnelId: ""                   # Optional: use an existing tunnel ID
  catchAll: "http_status:404"   # Default catch-all rule
  # API rate limit (requests/second). Cloudflare's official limit is about
  # 1200 requests / 5 minutes (≈4 RPS); the default of 4 matches it — going
  # higher will trigger 429 throttling.
  rateLimit: 4
  maxRetries: 3                  # Max retries
  retryDelay: 1s                 # Initial retry delay
  maxRetryDelay: 30s             # Max retry delay

controller:
  flappingWindow: 60s            # Flapping detection window
  flappingThreshold: 5           # Restart count that triggers flapping
  coolingPeriod: 300s            # Cooldown period (5 minutes)
  maxCoolingPeriod: 1800s        # Max cooldown period (30 minutes)
  debounceDuration: 2s           # Event debounce delay

cleanup:
  # Master switch for exit cleanup (default false). When false, no policy
  # cleans anything; when true the policy applies:
  # graceful-cleanup (clean up) | fast-exit (exit without cleaning).
  onExit: false
  strategy: "graceful-cleanup"

server:
  bindAddr: "127.0.0.1"  # Diagnostics/metrics bind address (loopback only by default)
  port: 9100             # Port (/metrics /healthz /debug/state)
  # debugToken: "..."    # Required when binding to a non-loopback address
                         # (e.g. 0.0.0.0), otherwise /debug/state would expose
                         # every hostname/service URL
```

#### Environment variables

All environment variables use the `DOCKTUNNEL_` prefix plus the config path
(dots become underscores), e.g. `cloudflare.accountId` →
`DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID`. Environment variables take precedence over
the config file.

```bash
export DOCKTUNNEL_LOG_LEVEL=debug
export DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID="your-account-id"
export DOCKTUNNEL_CLOUDFLARE_API_TOKEN="your-api-token"
export DOCKTUNNEL_CLOUDFLARE_TUNNEL_NAME="DockTunnel"
```

See [.env.example](.env.example) for the full variable list.

## Container Label System

### Label architecture

DockTunnel uses a two-layer label architecture:
- **Global rules**: Defaults applied to all services
- **Per-service rules**: Configuration for a specific service (higher precedence)

### Label format

```
docktunnel.<service-name>.<attribute>
```

For example, in `docktunnel.web.hostname`, `web` is the service name and
`hostname` is the attribute.

### Core labels

#### Required label

| Label | Type | Description | Example |
|-------|------|-------------|---------|
| `docktunnel.enable` | boolean | Enable switch (required) | `true` |

#### Global rule labels

| Label | Type | Description | Default |
|-------|------|-------------|---------|
| `docktunnel.traefik.enable` | boolean | Explicitly enable `traefik.*` label parsing (off by default) | `false` |
| `docktunnel.delete_retention` | string | Global retention policy (applies to all services) | `immediate` |

> `docktunnel.retention` is a **legacy alias** of `delete_retention`; both are
> equivalent. `delete_retention` is the documented form.

**Cleanup policy values**:
- `0` / `immediate`: delete immediately (default; deleted as soon as the container stops)
- `forever` / `keep`: keep forever
- `30m` / `1h` / `7d`: delete after a delay (time units: `s`, `m`, `h`, `d`)

#### Per-service labels

| Label | Type | Description | Example |
|-------|------|-------------|---------|
| `docktunnel.<name>.hostname` | string | Public hostname | `example.com` |
| `docktunnel.<name>.service` | string | Service address | `http://172.17.0.2:8080` |
| `docktunnel.<name>.path` | string | Path prefix | `/api` |
| `docktunnel.<name>.scheme` | string | Internal protocol | `https` |
| `docktunnel.<name>.port` | int | Internal port | `8080` |
| `docktunnel.<name>.network` | string | Docker network used to resolve the container IP (`host` means `localhost`) | `my-net` |
| `docktunnel.<name>.delete_retention` | string | Retention policy for this service (overrides global) | `30m` |

**Port detection fallback order**:
1. The `docktunnel.<name>.port` label
2. The Traefik `http.services.<name>.loadbalancer.server.port` label
3. The container's first exposed port
4. Default port `80`

### Origin Request settings

#### TLS

| Label | Type | Description |
|-------|------|-------------|
| `docktunnel.<name>.originRequest.noTLSVerify` | boolean | Skip TLS verification (allow self-signed certificates) |
| `docktunnel.<name>.originRequest.originServerName` | string | SNI hostname for the TLS handshake |
| `docktunnel.<name>.originRequest.caPool` | string | Path to the CA certificate bundle (must be mounted into the container) |

> Note: `matchSNItoHost` (including the legacy spelling `matchSniToHost`) has
> been removed — the Cloudflare API has no such field. Setting the label is
> ignored with a WARN log.

#### Timeouts

| Label | Type | Description | Default |
|-------|------|-------------|---------|
| `docktunnel.<name>.originRequest.connectTimeout` | duration | TCP connect timeout | `30s` |
| `docktunnel.<name>.originRequest.tlsTimeout` | duration | TLS handshake timeout | `10s` |
| `docktunnel.<name>.originRequest.tcpKeepAlive` | duration | TCP keep-alive probe interval | `30s` |

#### Connection pool

| Label | Type | Description | Default |
|-------|------|-------------|---------|
| `docktunnel.<name>.originRequest.keepAliveConnections` | int | Max idle connections | `100` |
| `docktunnel.<name>.originRequest.keepAliveTimeout` | duration | Idle connection timeout | `1m30s` |

#### HTTP

| Label | Type | Description |
|-------|------|-------------|
| `docktunnel.<name>.originRequest.httpHostHeader` | string | Force-rewrite the Host header |
| `docktunnel.<name>.originRequest.http2Origin` | boolean | Enable HTTP/2 (required for gRPC services) |
| `docktunnel.<name>.originRequest.disableChunkedEncoding` | boolean | Disable chunked transfer encoding |

#### Proxy

| Label | Type | Description |
|-------|------|-------------|
| `docktunnel.<name>.originRequest.proxyType` | string | Proxy type (usually empty or `socks`) |
| `docktunnel.<name>.originRequest.noHappyEyeballs` | boolean | Disable the Happy Eyeballs algorithm |

#### Cloudflare Access (Zero Trust)

| Label | Type | Description |
|-------|------|-------------|
| `docktunnel.<name>.access.required` | boolean | Enforce authentication (reject unverified requests) |
| `docktunnel.<name>.access.team_name` | string | Zero Trust team name |
| `docktunnel.<name>.access.aud_tag` | string | JWT Application Audience Tag |

> The following aliases are also accepted (same values): the
> `docktunnel.<name>.originRequest.access.*` prefix, and the camelCase
> spellings `access.teamName` / `access.audTag`.

### Traefik-Compatible Labels

DockTunnel can parse Traefik labels, but **only an optional minimal compatible
subset**, which must be enabled explicitly with
`docktunnel.traefik.enable=true` (by default no `traefik.*` labels are parsed,
so that Traefik-internal or middleware-protected routes are never published
accidentally as public rules).

**Supported**:

| Traefik label | Parsing behavior |
|---------------|------------------|
| `traefik.http.routers.<name>.rule` | Extracts `Host(...)` and `Path(...)` clauses → hostname / path |
| `traefik.http.services.<name>.loadbalancer.server.port` | Origin port |
| `traefik.http.services.<name>.loadbalancer.server.scheme` | Origin scheme |
| `traefik.http.services.<name>.loadbalancer.server.url` | Origin URL (**takes precedence over** port/scheme) |
| `traefik.tcp.routers.<name>.rule` | Extracts `HostSNI(...)` → TCP rule |
| `traefik.tcp.services.<name>.loadbalancer.server.port` | TCP origin port |
| `traefik.docker.network` | Docker network used to resolve the container IP (same as `docktunnel.<svc>.network`) |

**Rejected with WARN** (cannot be mapped safely; the router is skipped and
logged with WARN, never exposed):

- `!` (negation), `&&`, `||` (boolean operators)
- `HostRegexp`, `PathPrefix`, `PathRegexp`
- `Method`, `Header`, `Query`, `ClientIP`
- `middlewares` (routers referencing middlewares are rejected — avoids exposing
  auth-protected routes without authentication)
- `weighted` services, and routers referencing a service that does not exist or
  has no `loadbalancer.server` (otherwise they would silently degrade into
  portless routes)

**Benignly ignored** (no security impact; Info log only, produces no route):

- `entryPoints`, `priority`, `tls`, UDP

**Other behavior**:
- A router with only `Path(...)` and no `Host(...)` clause is skipped with a
  WARN; an unquoted rule like `Host(a.com)` is treated as having no hostname
  and is also skipped with a WARN;
- TCP rules only allow `HostSNI(...)`; mixing in `Host(...)`/`Path(...)` is rejected;
- Anything other than `Host(...)`/`Path(...)` in an HTTP rule is rejected.

```bash
# Minimal example enabling Traefik-compatible parsing
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('legacy.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  legacy-app:latest
```

### Container IP detection

DockTunnel detects the container IP automatically:

- **Host network mode**: uses `localhost`
- **Bridge network**: uses the container's bridge IP (e.g. `172.17.0.2`)
- **Other networks**: uses the first available network's IP (networks sorted by
  name, so the result is deterministic)

You can specify the network explicitly with `docktunnel.<svc>.network` (or
Traefik's `traefik.docker.network`); the value `host` means `localhost`. If the
network does not exist, a warning is logged and default detection is used.

### Full label example

```bash
docker run -d \
  --name=web-app \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=app.example.com \
  -l docktunnel.web.service=http://172.17.0.2:8080 \
  -l docktunnel.web.path=/api \
  -l docktunnel.web.originRequest.noTLSVerify=true \
  -l docktunnel.web.originRequest.connectTimeout=30s \
  -l docktunnel.web.originRequest.keepAliveConnections=50 \
  -l docktunnel.web.access.required=true \
  -l docktunnel.web.access.team_name=myteam \
  nginx:latest
```

## Examples

### Example 1: Basic web service

```bash
docker run -d \
  --name=my-web-app \
  -p 8080:80 \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=myapp.example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  nginx:alpine
```

**What happens automatically**:
1. The container start event is detected
2. Labels are parsed into ingress rules
3. The tunnel configuration is created/updated in Cloudflare
4. A DNS record `myapp.example.com` is created pointing at the tunnel
5. The configuration takes effect and the service is reachable from outside

### Example 2: Multi-service container

Expose multiple services from one container:

```bash
docker run -d \
  --name=fullstack-app \
  -l docktunnel.enable=true \
  -l docktunnel.frontend.hostname=app.example.com \
  -l docktunnel.frontend.service=http://localhost:3000 \
  -l docktunnel.api.hostname=api.example.com \
  -l docktunnel.api.service=http://localhost:8080 \
  -l docktunnel.api.path=/api \
  -l docktunnel.api.originRequest.http2Origin=true \
  myapp:latest
```

> Here `frontend` is the frontend service and `api` is the API service (with
> the `/api` path and HTTP/2).

### Example 3: Traefik-compatible mode

> `docktunnel.traefik.enable=true` must be set explicitly for `traefik.*`
> labels to be parsed.

```bash
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('legacy.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  -l traefik.http.services.app.loadbalancer.server.scheme=http \
  legacy-app:latest
```

### Example 4: Self-signed certificate + gRPC service

```bash
docker run -d \
  --name=grpc-service \
  -l docktunnel.enable=true \
  -l docktunnel.grpc.hostname=grpc.example.com \
  -l docktunnel.grpc.service=https://localhost:9090 \
  -l docktunnel.grpc.originRequest.noTLSVerify=true \
  -l docktunnel.grpc.originRequest.http2Origin=true \
  -l docktunnel.grpc.originRequest.originServerName=grpc.example.com \
  grpc-service:latest
```

### Example 5: Delayed cleanup policy

```bash
docker run -d \
  --name=temp-service \
  -l docktunnel.enable=true \
  -l docktunnel.temp.hostname=temp.example.com \
  -l docktunnel.temp.service=http://localhost:8080 \
  -l docktunnel.temp.delete_retention=30m \
  temp-service:latest
```

**Cleanup flow**:
1. After the container stops, the configuration is kept for 30 minutes
2. The global GC job scans once per minute
3. After 30 minutes the configuration and DNS records are deleted automatically

### Example 6: Permanent retention policy

```bash
docker run -d \
  --name=prod-service \
  -l docktunnel.enable=true \
  -l docktunnel.prod.hostname=prod.example.com \
  -l docktunnel.prod.service=http://localhost:8080 \
  -l docktunnel.prod.delete_retention=forever \
  prod-service:latest
```

**Behavior**: after the container stops, the configuration is kept forever
(ignored by GC scans)

### Example 7: Cloudflare Access zero-trust protection

```bash
docker run -d \
  --name=internal-tool \
  -l docktunnel.enable=true \
  -l docktunnel.tool.hostname=internal.example.com \
  -l docktunnel.tool.service=http://localhost:3000 \
  -l docktunnel.tool.access.required=true \
  -l docktunnel.tool.access.team_name=engineering \
  -l docktunnel.tool.access.aud_tag=a4b3c2d1 \
  internal-tool:latest
```

**Behavior**: requests must pass Cloudflare Access verification

### Example 8: Full configuration (all options)

Covers every option for service, TLS, timeouts, connection pool, HTTP, proxy,
Access, and cleanup policy:

```bash
docker run -d \
  --name=full-config \
  -l docktunnel.enable=true \
  -l docktunnel.full.hostname=full.example.com \
  -l docktunnel.full.service=http://172.17.0.2:8080 \
  -l docktunnel.full.path=/api \
  -l docktunnel.full.originRequest.noTLSVerify=true \
  -l docktunnel.full.originRequest.originServerName=origin.example.com \
  -l docktunnel.full.originRequest.caPool=/etc/ssl/certs/ca.pem \
  -l docktunnel.full.originRequest.connectTimeout=30s \
  -l docktunnel.full.originRequest.tlsTimeout=10s \
  -l docktunnel.full.originRequest.tcpKeepAlive=30s \
  -l docktunnel.full.originRequest.keepAliveConnections=100 \
  -l docktunnel.full.originRequest.keepAliveTimeout=90s \
  -l docktunnel.full.originRequest.httpHostHeader=full.example.com \
  -l docktunnel.full.originRequest.http2Origin=false \
  -l docktunnel.full.originRequest.disableChunkedEncoding=false \
  -l docktunnel.full.originRequest.proxyType=socks \
  -l docktunnel.full.originRequest.noHappyEyeballs=false \
  -l docktunnel.full.access.required=true \
  -l docktunnel.full.access.team_name=myteam \
  -l docktunnel.full.access.aud_tag=abc123 \
  -l docktunnel.full.delete_retention=1h \
  full-config:latest
```

## Advanced Features

### Flapping detection

When a container restarts frequently within a short time, DockTunnel will:

1. **Detect flapping**: more than `flappingThreshold` (default 5) restarts
   within `flappingWindow` (default 60s)
2. **Enter cooldown**: mark the container as unstable and start a cooldown period
3. **Back off exponentially**: the cooldown starts at `coolingPeriod`
   (default 300s) up to `maxCoolingPeriod` (default 1800s)
4. **Resume syncing**: once the cooldown ends, normal syncing resumes

### Event debouncing

- **Debounce delay**: `debounceDuration` (default 2s)
- **Behavior**: multiple container events within 2 seconds are coalesced into a single sync
- **Benefit**: fewer API calls, no config thrashing

### State sync flow

```
container start → parse labels → validate rules → update in-memory state → batch-sync Cloudflare
    ↓
change detected → compute config diff → apply update → log
```

### Rule validation

- **Hostname uniqueness**: checked globally to prevent domain conflicts
- **Service name uniqueness**: checked per container
- **Required fields**: ensures `hostname` and `service` are fully configured

## Configuration Precedence

### Port detection precedence

1. The `docktunnel.<name>.port` label
2. The `traefik.http.services.<name>.loadbalancer.server.port` label
3. The container's first exposed port
4. Default port `80`

### Scheme detection precedence

1. The scheme embedded in the `docktunnel.<name>.service` label (e.g. `https://`)
2. The `docktunnel.<name>.scheme` label
3. The `traefik.http.services.<name>.loadbalancer.server.scheme` label
4. Default scheme `http`

### Config merge precedence

1. **Per-service rules**: `docktunnel.<name>.*` labels (highest)
2. **Traefik-compatible**: `traefik.http.*` labels
3. **Global rules**: `docktunnel.*` labels (without a service name)
4. **System defaults**: built-in defaults

## Troubleshooting

### Debug mode

```bash
# Enable debug logging
export DOCKTUNNEL_LOG_LEVEL=debug
./docktunnel

# Or in the config file
log:
  level: debug
  format: json
```

### Common issues

#### 1. Cannot connect to the Docker daemon

**Symptom**: `Error: Cannot connect to the Docker daemon`

**Fixes**:
- Make sure the Docker socket is mounted: `-v /var/run/docker.sock:/var/run/docker.sock`
- Check permissions: `ls -l /var/run/docker.sock`
- Make sure DockTunnel runs outside the Docker container or the socket is mounted correctly

#### 2. Cloudflare API errors

**Symptom**: `Error: Cloudflare API request failed`

**Fixes**:
- Verify `accountId` and `apiToken`
- Confirm the API token permissions:
  - Account: Read/Write
  - Zone: Read/Write
  - Tunnel: Read/Write
- Check network connectivity and firewall settings

#### 3. Container labels have no effect

**Symptom**: nothing happens after the container starts

**Fixes**:
- Confirm `docktunnel.enable=true` is set
- Check the logs: `docker logs docktunnel`
- Verify the label format: `docker inspect <container> --format='{{json .Config.Labels}}'`
- Make sure `hostname` and `service` are configured correctly

#### 4. DNS records are not created

**Symptom**: tunnel config succeeds but the domain is unreachable

**Fixes**:
- Check whether the DNS record shows up in the Cloudflare dashboard
- Verify the Zone ID
- Confirm the domain has been added to your Cloudflare account
- Check DNS propagation: `dig example.com`

#### 5. Service is unreachable

**Symptom**: DNS resolves but the service cannot be reached

**Fixes**:
- Verify the container service is running: `docker exec <container> curl localhost:8080`
- Check the container IP: `docker inspect <container> --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'`
- Confirm the network mode:
  - Bridge mode: the container IP is used
  - Host mode: `localhost` is used
- Check firewall rules

#### 6. Config is updated/reverted frequently

**Symptom**: the Cloudflare configuration changes constantly

**Fixes**:
- Check whether containers are restarting frequently (flapping)
- Tune `flappingThreshold` and `coolingPeriod`
- Check the event debounce setting `debounceDuration`
- Look for cooldown hints in the logs

### Log analysis

#### Debug log example

```json
{
  "level": "DEBUG",
  "msg": "Container started",
  "container_id": "abc123",
  "container_name": "web-app",
  "labels": {
    "docktunnel.enable": "true",
    "docktunnel.web.hostname": "app.example.com"
  }
}
```

#### Error log example

```json
{
  "level": "ERROR",
  "msg": "Failed to update tunnel configuration",
  "error": "rate limit exceeded",
  "retry_after": "60s"
}
```

## Development Guide

### Project layout

```
DockTunnel/
├── cmd/
│   └── docktunnel/          # Main entry point
│       ├── main.go
│       └── main_test.go
├── internal/
│   ├── cloudflareManager/   # Cloudflare API management (tunnel/DNS/zone)
│   │   ├── tunnel.go
│   │   └── tunnel_test.go
│   ├── config/              # Config management (file + env vars + defaults)
│   │   ├── config.go
│   │   └── config_test.go
│   ├── controller/          # Core business logic (state machine/reconcile/GC/compensation)
│   │   ├── controller.go    # Orchestrator
│   │   ├── dispatcher.go    # Event dispatch
│   │   ├── syncer.go        # State sync
│   │   ├── reconciler.go    # Periodic reconciliation
│   │   ├── handlers.go      # Container lifecycle handling
│   │   ├── health.go        # Flapping detection
│   │   ├── gc.go            # Expired-resource cleanup
│   │   ├── compensation.go  # Failure compensation
│   │   ├── diagnostics.go   # Diagnostics data
│   │   ├── sync_worker.go   # Serialized Cloudflare writes
│   │   └── validator.go     # Rule validation
│   ├── docker/              # Docker monitoring (events/reconnect/scan)
│   │   ├── monitor.go
│   │   └── monitor_test.go
│   ├── events/              # Event definitions
│   │   └── event.go
│   ├── label/               # Label parsing (docktunnel.* + Traefik-compatible)
│   │   ├── parser.go
│   │   ├── builder.go
│   │   ├── docktunnel.go
│   │   ├── traefik.go
│   │   └── types.go
│   ├── state/               # State management (persistence/state machine/compensation queue)
│   │   ├── manager.go
│   │   ├── persistence.go
│   │   ├── transition.go
│   │   └── compensation.go
│   ├── instance/            # Single-instance lock
│   ├── metrics/             # Prometheus metrics
│   ├── diagnostics/         # /debug/state diagnostics
│   ├── server/              # HTTP server (/metrics /healthz /debug/state)
│   └── logger/              # Structured logging
├── pkg/types/               # Public types (error classification/Tunnel types)
├── tests/                   # Integration tests + mocks
├── config.yaml              # Example config file
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
├── docker-compose.yml
└── README.md
```

### Building

```bash
# Format code
make fmt

# Download dependencies
make deps

# Build
make build

# Run tests
make test

# Test coverage
make test-coverage

# Clean
make clean
```

### Testing

```bash
# Run all tests
go test ./...

# Run one package's tests
go test ./internal/controller

# Verbose output
go test -v ./internal/controller

# Coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Code style

- Follow the [Effective Go](https://golang.org/doc/effective_go) guidelines
- Format with `gofmt`
- Write unit tests (coverage target: 80%+)
- Add doc comments to exported types, functions, and constants

### Submitting code

1. Fork the project
2. Create a feature branch: `git checkout -b feature/amazing-feature`
3. Commit changes: `git commit -m 'Add amazing feature'`
4. Push the branch: `git push origin feature/amazing-feature`
5. Open a Pull Request

## Deployment

### As a system service (systemd)

Create `/etc/systemd/system/docktunnel.service`:

```ini
[Unit]
Description=DockTunnel - Cloudflare Tunnel Docker Controller
After=docker.service
Requires=docker.service

[Service]
Type=simple
# Run as a non-root user (principle of least privilege)
User=docktunnel
ExecStart=/usr/local/bin/docktunnel
Restart=always
RestartSec=10
# CONFIG_PATH gives the full path of the config file (supported by the config
# loader); useful for systemd deployments (./config.yaml is not looked up)
Environment=CONFIG_PATH=/etc/docktunnel/config.yaml

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
# State directory: with ProtectSystem=strict, /var/lib is read-only and must
# be allowed explicitly. StateDirectory creates /var/lib/docktunnel owned by User
StateDirectory=docktunnel
ReadWritePaths=/var/lib/docktunnel

[Install]
WantedBy=multi-user.target
```

Enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable docktunnel
sudo systemctl start docktunnel
sudo systemctl status docktunnel
```

### Docker Compose deployment

```yaml
services:
  docktunnel:
    image: ghcr.io/k997/docktunnel:latest
    container_name: docktunnel
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - docktunnel-state:/var/lib/docktunnel   # Persistent state, survives container recreation
      - ./config.yaml:/etc/docktunnel/config.yaml:ro
    environment:
      - DOCKTUNNEL_LOG_LEVEL=info
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1:9100/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

volumes:
  docktunnel-state:
```

A ready-to-use [docker-compose.yml](docker-compose.yml) is included in the repository.

### Kubernetes deployment (DaemonSet)

> **Example not verified** — validate in a test cluster before production use
> (label parsing requires the Docker socket; in DaemonSet mode verify the
> `hostPath` mount and node permissions).

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: docktunnel
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: docktunnel
  template:
    metadata:
      labels:
        app: docktunnel
    spec:
      containers:
      - name: docktunnel
        image: ghcr.io/k997/docktunnel:latest
        resources:
          limits:
            memory: "128Mi"
            cpu: "500m"
        volumeMounts:
        - name: docker-socket
          mountPath: /var/run/docker.sock
          readOnly: true
        - name: config
          mountPath: /etc/docktunnel
          readOnly: true
        env:
        - name: DOCKTUNNEL_LOG_LEVEL
          value: "info"
      volumes:
      - name: docker-socket
        hostPath:
          path: /var/run/docker.sock
      - name: config
        configMap:
          name: docktunnel-config
```

## Performance Tuning

### API rate limiting

Defaults:
- Rate limit: 4 requests/second (Cloudflare's official API limit is about
  1200 requests / 5 minutes, ≈4 RPS; the default matches it — exceeding it
  triggers 429)
- Max retries: 3
- Retry delay: 1s (exponential growth, capped at 30s)

Recommendations:
- Large deployments (100+ containers): can be raised to 10-20 requests/second
  (note that Cloudflare's official 429 throttling may still kick in; adjust
  based on actual usage)
- Small deployments (< 20 containers): keep the default of 4 requests/second

### Event debounce tuning

- **Default**: 2 seconds
- **Highly dynamic environments**: lower to 500ms - 1s
- **Stable environments**: raise to 5s - 10s

### Cooldown tuning

- **Default**: 300s (5 minutes)
- **Production**: raise to 600s - 900s
- **Development**: lower to 60s - 120s

## Security Recommendations

### API token security

- Apply the principle of least privilege
- Rotate API tokens regularly
- Never commit tokens to version control
- Use environment variables or a secret manager (e.g. HashiCorp Vault)

### Docker socket security

- Mount read-only: `/var/run/docker.sock:ro`
- Limit container capabilities (do not use `--privileged`)
- Run under a dedicated user

### Network isolation

- Run DockTunnel on a dedicated network
- Restrict outbound connections (only the Cloudflare API is needed)
- Use firewall rules to limit access

## Common Use Cases

### Use case 1: local development environment

Expose a local dev service to the internet:

```bash
docker run -d \
  --name=dev-app \
  -p 3000:3000 \
  -l docktunnel.enable=true \
  -l docktunnel.dev.hostname=dev.example.com \
  -l docktunnel.dev.service=http://localhost:3000 \
  my-dev-app:latest
```

### Use case 2: microservice architecture

Manage external access to multiple microservices:

```bash
# User service
docker run -d --name=user-service \
  -l docktunnel.enable=true \
  -l docktunnel.users.hostname=api.example.com \
  -l docktunnel.users.path=/users \
  -l docktunnel.users.service=http://user-service:8001 \
  user-service:latest

# Order service
docker run -d --name=order-service \
  -l docktunnel.enable=true \
  -l docktunnel.orders.hostname=api.example.com \
  -l docktunnel.orders.path=/orders \
  -l docktunnel.orders.service=http://order-service:8002 \
  order-service:latest
```

### Use case 3: CI/CD pipelines

Temporarily expose a test environment:

```bash
docker run -d \
  --name=staging-$BUILD_NUMBER \
  -l docktunnel.enable=true \
  -l docktunnel.staging.hostname=staging-$BUILD_NUMBER.example.com \
  -l docktunnel.staging.service=http://localhost:8080 \
  -l docktunnel.staging.delete_retention=1h \
  staging-app:latest
```

### Use case 4: migrating from Traefik

Migrate from Traefik to Cloudflare Tunnel (Traefik label parsing must be
enabled explicitly):

```bash
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('app.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  legacy-app:latest
```

## Monitoring & Logging

### Diagnostics and monitoring endpoints

The diagnostics/metrics HTTP server binds to `127.0.0.1:9100` by default and
serves three endpoints:

| Endpoint | Description |
|----------|-------------|
| `/healthz` | Liveness probe (returns 200; both the Docker HEALTHCHECK and the compose healthcheck hit this) |
| `/metrics` | Prometheus metrics |
| `/debug/state` | Snapshot of the current desired state (**exposes every hostname and service URL**) |

Security notes:
- By default it listens on loopback only (`127.0.0.1`) and needs no extra protection;
- If you change `server.bindAddr` to a non-loopback address (e.g. `0.0.0.0`),
  you **must** configure `server.debugToken` (environment variable
  `DOCKTUNNEL_SERVER_DEBUG_TOKEN`) — startup validation rejects otherwise, and
  `/debug/state` requires a Bearer token.

### Log output example

```json
{"level":"INFO","msg":"DockTunnel starting","version":"1.0.0"}
{"level":"INFO","msg":"Connected to Docker daemon"}
{"level":"INFO","msg":"Connected to Cloudflare API","account_id":"xxx"}
{"level":"DEBUG","msg":"Container event received","action":"start","container_id":"abc123"}
{"level":"INFO","msg":"Processing container","container_name":"web-app","services":["web"]}
{"level":"INFO","msg":"Updating tunnel configuration","tunnel_id":"xxx","rules_count":5}
{"level":"INFO","msg":"DNS records updated","created":1,"deleted":0}
{"level":"INFO","msg":"Sync completed","duration":1.234s}
```

### Log aggregation

Recommended log aggregation tools:
- **ELK Stack**: Elasticsearch + Logstash + Kibana
- **Loki**: Grafana Loki (lightweight)
- **Fluentd**: Fluentd + Elasticsearch
- **Cloud Logging**: your cloud provider's logging service

## Contributing

Contributions are welcome! Please follow these steps:

1. Fork the project
2. Create a feature branch: `git checkout -b feature/amazing-feature`
3. Write tests: `go test ./...`
4. Commit: `git commit -m 'Add amazing feature'`
5. Push the branch: `git push origin feature/amazing-feature`
6. Open a Pull Request

### Code review standards

- Follow Go coding conventions
- Test coverage > 80%
- Add doc comments
- All tests pass

## License

This project is licensed under the **MIT License**. See the [LICENSE](LICENSE)
file for details.

License notices for third-party code can be found in
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).

## Acknowledgments

- [Cloudflare](https://www.cloudflare.com/) - Tunnel service and API
- [Cloudflare Go SDK](https://github.com/cloudflare/cloudflare-go) - official Go SDK
- [Docker](https://www.docker.com/) - container technology
- [Traefik](https://traefik.io/) - inspiration for the label compatibility design

## Contact

- **Issues**: [GitHub Issues](https://github.com/k997/docktunnel/issues)
- **Feature requests**: [GitHub Discussions](https://github.com/k997/docktunnel/discussions)

---

**Note**: This project is under active development and the API may change.
Thorough testing is recommended before production use.

**Star 🌟 this project**: if you find DockTunnel helpful, please give us a star!
