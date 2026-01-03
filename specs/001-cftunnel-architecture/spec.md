# Feature Specification: Cloudflare Tunnel Docker Controller

**Feature Branch**: `001-cftunnel-architecture`
**Created**: 2026-01-03
**Status**: Draft
**Input**: Cloudflare Tunnel Docker Controller with event-driven architecture, Traefik compatibility, and state management

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Automatic Container Exposure (Priority: P1)

As a DevOps engineer, I want Docker containers to be automatically exposed through Cloudflare Tunnel when they start, so that I don't have to manually configure tunnel routes every time I deploy or restart services.

**Why this priority**: This is the core value proposition - eliminating manual tunnel configuration. Without this, the system provides no benefit over manual Cloudflare Tunnel management.

**Independent Test**: Can be fully tested by starting a labeled Docker container and verifying it becomes accessible via its configured hostname through Cloudflare Tunnel without any manual intervention.

**Acceptance Scenarios**:

1. **Given** a Docker container with `docktunnel.enable=true` and `docktunnel.web.hostname=app.example.com` labels, **When** the container starts, **Then** the service should be accessible via `https://app.example.com` through Cloudflare Tunnel within 5 seconds
2. **Given** a running container with tunnel configuration, **When** the container stops, **Then** the tunnel route should be removed or marked for retention based on the retention policy
3. **Given** multiple containers with different hostnames, **When** they start simultaneously, **Then** all should be accessible through their respective hostnames
4. **Given** a container without the required `docktunnel.enable=true` label, **When** it starts, **Then** no tunnel configuration should be created

---

### User Story 2 - Traefik Compatibility Layer (Priority: P2)

As a user migrating from Traefik, I want to reuse my existing Traefik labels for Cloudflare Tunnel configuration, so that I can adopt this tool without rewriting all my container labels.

**Why this priority**: Significant adoption blocker - many potential users already use Traefik. This provides a smooth migration path and reduces switching costs.

**Independent Test**: Can be fully tested by deploying a container with only Traefik labels (no DockTunnel-specific labels) and verifying it generates correct tunnel configuration.

**Acceptance Scenarios**:

1. **Given** a container with Traefik labels `traefik.http.routers.myapp.rule=Host(\`app.example.com\`)` and `traefik.http.services.myapp.loadbalancer.server.port=8080`, **When** the container starts, **Then** a tunnel route should be created for `app.example.com` pointing to the container's service
2. **Given** a container with both DockTunnel and Traefik labels, **When** configuration is resolved, **Then** DockTunnel labels should take precedence over Traefik labels
3. **Given** a container with incomplete Traefik labels (missing port), **When** auto-detection runs, **Then** the system should detect the first exposed port and complete the configuration

---

### User Story 3 - Configuration Retention Policies (Priority: P2)

As a platform operator, I want stopped containers' tunnel routes to be retained for a configurable period, so that temporary container restarts or deployments don't immediately break DNS propagation and user access.

**Why this priority**: Critical for production stability - prevents configuration churn during deployments or container restarts, which could cause intermittent service unavailability.

**Independent Test**: Can be fully tested by stopping a container with a retention policy and verifying the tunnel route remains active for the specified duration before being removed.

**Acceptance Scenarios**:

1. **Given** a container with `docktunnel.delete_retention=30m` label, **When** the container stops, **Then** the tunnel route should remain active for 30 minutes before being removed
2. **Given** a container with `docktunnel.delete_retention=immediate` label, **When** the container stops, **Then** the tunnel route should be removed immediately
3. **Given** a container with `docktunnel.delete_retention=forever` label, **When** the container stops, **Then** the tunnel route should never be automatically removed
4. **Given** a container marked for retention, **When** the same container restarts within the retention period, **Then** the existing tunnel route should be reused and the retention timer cancelled

---

### User Story 4 - Configuration Priority Fallback (Priority: P1)

As a user configuring container tunnels, I want the system to automatically detect missing configuration values (like ports, IPs, protocols), so that I don't need to specify every single detail in labels.

**Why this priority**: Essential for usability - reduces configuration boilerplate and makes the system work with minimal labels. Without this, every container would need extensive configuration.

**Independent Test**: Can be fully tested by starting containers with partial configuration and verifying the system automatically fills in missing values from increasingly lower-priority sources.

**Acceptance Scenarios**:

1. **Given** a container with only a hostname label (no port), **When** configuration resolves, **Then** the system should detect the first exposed Docker port and use it
2. **Given** a container with hostname and port labels (no service URL), **When** configuration resolves, **Then** the system should construct the service URL from detected IP, protocol (default HTTP), and specified port
3. **Given** a container with partial Traefik labels (hostname only), **When** configuration resolves, **Then** the system should extract the hostname from Traefik rules and detect ports from container inspection
4. **Given** a container with no custom origin request settings, **When** configuration resolves, **Then** global defaults from the configuration file should be applied

---

### User Story 5 - State Persistence and Recovery (Priority: P3)

As a system administrator, I want the controller to remember retention policies across restarts, so that temporary controller failures don't cause premature removal of tunnel routes for stopped containers.

**Why this priority**: Important for production resilience - prevents data loss during controller restarts. However, with a reasonable default retention policy, the system can function without this (though with potentially premature route removal).

**Independent Test**: Can be fully tested by stopping a container with a retention policy, restarting the controller, and verifying the retention timer is preserved.

**Acceptance Scenarios**:

1. **Given** a container stopped with a 30-minute retention timer, **When** the controller restarts after 10 minutes, **Then** the remaining 20 minutes should be honored before route removal
2. **Given** a controller with persisted state, **When** it starts, **Then** it should reconcile current running containers with persisted state and detect any containers that started while it was down
3. **Given** corrupted state file, **When** the controller starts, **Then** it should fall back to a full container scan and log an error rather than failing to start

---

### User Story 6 - Advanced Origin Request Configuration (Priority: P3)

As a security-conscious operator, I want fine-grained control over TLS verification, timeouts, and Cloudflare Access policies, so that I can enforce security policies and integrate with my organization's SSO.

**Why this priority**: Important for enterprise adoption but not required for basic functionality. Many users will succeed with default settings.

**Independent Test**: Can be fully tested by configuring a container with origin request labels and verifying the Cloudflare Tunnel configuration includes the specified settings.

**Acceptance Scenarios**:

1. **Given** a container with `docktunnel.web.originRequest.noTLSVerify=true` label, **When** tunnel is configured, **Then** the route should skip TLS verification to the origin
2. **Given** a container with `docktunnel.web.originRequest.connectTimeout=30s` label, **When** tunnel is configured, **Then** Cloudflare should wait up to 30 seconds when connecting to the origin
3. **Given** a container with `docktunnel.web.originRequest.access.required=true` and team name, **When** users access the service, **Then** they should be prompted for Cloudflare Access authentication
4. **Given** global defaults for origin settings, **When** a container doesn't specify origin request labels, **Then** the global defaults should be applied

---

### Edge Cases

1. **Container Flapping**: What happens when a container rapidly starts and stops within seconds?
   - System should implement a flapping detection window (configurable, default 60s) and exponential backoff to prevent configuration thrashing

2. **Conflicting Hostnames**: What happens when two containers specify the same hostname?
   - System should validate hostname uniqueness and reject new configurations with conflicting hostnames, logging a clear error

3. **Network Mode Variations**: How does the system handle containers using host networking vs. bridge networking?
   - System should auto-detect network mode and use `localhost` for host networking or container IP for bridge networking

4. **Cloudflare API Rate Limits**: What happens when Cloudflare API requests are rate-limited?
   - System should implement token bucket rate limiting and exponential backoff with retry (up to configurable max retries)

5. **Container Without Exposed Ports**: What happens when a container has no exposed ports defined?
   - System should skip the container and log a warning that no ports could be detected

6. **Invalid Service URLs**: What happens when a specified service URL is malformed?
   - System should validate URLs during parsing and reject invalid configurations with a clear error message

7. **Controller Shutdown During Sync**: What happens if the controller is killed while updating Cloudflare configuration?
   - System should implement graceful shutdown that completes in-progress syncs or marks state for reconciliation on next start

8. **Multiple Containers Same Service**: What happens when multiple containers on the same network expose the same service (load balancing scenario)?
   - System MUST reject duplicate hostnames across containers with a clear error. Each hostname must be unique across all containers. Load balancing should be handled externally (Cloudflare Load Balancer, DNS round-robin, or reverse proxy containers)

## Requirements *(mandatory)*

### Functional Requirements

#### Core Functionality
- **FR-001**: System MUST automatically monitor Docker daemon events (container start, stop, die) for containers with `docktunnel.enable=true` label
- **FR-002**: System MUST create Cloudflare Tunnel ingress rules within 5 seconds of container start
- **FR-003**: System MUST remove or mark for deletion Cloudflare Tunnel ingress rules within 5 seconds of container stop (based on retention policy)
- **FR-004**: System MUST validate that all required configuration fields (hostname, service URL) are present before creating tunnel rules
- **FR-005**: System MUST prevent duplicate hostname configurations across all containers

#### Configuration Resolution (4-Layer Priority)
- **FR-006**: System MUST parse DockTunnel-specific labels (`docktunnel.<name>.<attribute>`) as the highest priority configuration source
- **FR-007**: System MUST parse and extract configuration from Traefik labels (`traefik.http.routers.*`, `traefik.http.services.*`) as fallback when DockTunnel labels are incomplete
- **FR-008**: System MUST auto-detect container IP addresses from Docker network settings when not explicitly configured
- **FR-009**: System MUST auto-detect exposed container ports when port is not specified in labels
- **FR-010**: System MUST apply global configuration defaults (timeouts, TLS settings, etc.) when not specified in container labels

#### Network and Service Detection
- **FR-011**: System MUST detect container network mode (host vs bridge) and use appropriate service URL format
- **FR-012**: System MUST use `localhost` as the service address for host networked containers
- **FR-013**: System MUST use the container's bridge network IP address for bridge networked containers
- **FR-014**: System MUST extract hostname from Traefik `Host()` regex patterns when parsing Traefik labels
- **FR-015**: System MUST construct service URLs in the format `http://<ip>:<port>` or `https://<ip>:<port>` based on detected or specified protocol

#### Retention and Garbage Collection
- **FR-016**: System MUST support three retention modes: `immediate`, `<duration>` (e.g., `30m`, `1h`), and `forever`
- **FR-017**: System MUST track deletion timestamps for stopped containers with timed retention policies
- **FR-018**: System MUST run a garbage collection process at least every 60 seconds to remove expired retention entries
- **FR-019**: System MUST cancel retention timers and restore routes when a container restarts within its retention period
- **FR-020**: System MUST persist retention state to disk and restore it on controller restart

#### State Management and Synchronization
- **FR-021**: System MUST maintain an in-memory map of all active tunnel configurations keyed by container ID
- **FR-022**: System MUST perform a full container scan on startup to detect containers that started while the controller was down
- **FR-023**: System MUST reconcile desired state (from containers) with actual state (in Cloudflare) at least every 30 seconds
- **FR-024**: System MUST implement debouncing to batch rapid container state changes within a 2-second window

#### Cloudflare Integration
- **FR-025**: System MUST authenticate with Cloudflare API using provided account ID and API token
- **FR-026**: System MUST fetch existing Cloudflare Tunnel configuration and calculate differences before applying updates
- **FR-027**: System MUST update Cloudflare Tunnel ingress rules using batch API operations
- **FR-028**: System MUST ensure catch-all routes always appear last in the ingress rules array
- **FR-029**: System MUST implement exponential backoff retry logic for Cloudflare API failures (up to 3 retries by default)
- **FR-030**: System MUST implement rate limiting using token bucket algorithm (default 10 requests per second)

#### Origin Request Configuration
- **FR-031**: System MUST support TLS verification control (noTLSVerify) via container labels or global defaults
- **FR-032**: System MUST support connection timeout configuration (connectTimeout) via container labels or global defaults
- **FR-033**: System MUST support TCP keep-alive configuration (keepAliveConnections, keepAliveTimeout) via container labels or global defaults
- **FR-034**: System MUST support HTTP/2 configuration (http2Origin) via container labels or global defaults
- **FR-035**: System MUST support Cloudflare Access integration (required, teamName, audTag) via container labels or global defaults
- **FR-036**: System MUST support proxy protocol configuration (proxyAddress, proxyPort) via container labels or global defaults

#### Error Handling and Logging
- **FR-037**: System MUST log all configuration changes at INFO level
- **FR-038**: System MUST log all errors at ERROR level with sufficient context for troubleshooting
- **FR-039**: System MUST continue processing other containers when one container's configuration fails
- **FR-040**: System MUST validate all container labels on startup and reject containers with invalid configurations with clear error messages

#### Configuration Management
- **FR-041**: System MUST load configuration from YAML files in the following priority order: `/etc/docktunnel/config.yaml`, `./config.yaml`
- **FR-042**: System MUST support environment variable overrides for all configuration values (e.g., `DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID`)
- **FR-043**: System MUST provide sensible defaults for all optional configuration values
- **FR-044**: System MUST validate required configuration values (Cloudflare account ID, API token, tunnel name) on startup

#### Fault Tolerance
- **FR-045**: System MUST detect container flapping (rapid start/stop cycles) and implement exponential backoff for flapping containers
- **FR-046**: System MUST mark flapping containers for cooling period (default 300s) during which no tunnel configuration changes are made
- **FR-047**: System MUST implement graceful shutdown that completes in-progress Cloudflare API operations
- **FR-048**: System MUST optionally clean up all Cloudflare Tunnel routes on exit (configurable via cleanup.onExit)

### Key Entities

#### Container Event
- Represents a Docker container lifecycle event (start, stop, die)
- Attributes: container ID, timestamp, event type, actor attributes
- Used to trigger configuration reconciliation

#### Tunnel Configuration
- Represents a complete set of labels and metadata for a single container service tunnel
- Attributes: hostname, service URL, path, origin request settings, container ID, service name
- Mapped 1:1 to Cloudflare Tunnel ingress rules

#### Tunnel Entry
- Represents a single tunnel route with lifecycle management state
- Attributes: container ID, tunnel ID, ingress rule, retention policy, deletion timestamp
- Managed by StateManager with garbage collection

#### Ingress Rule
- Represents a standardized Cloudflare Tunnel ingress configuration
- Attributes: hostname, path, service URL, origin request configuration
- Converted to Cloudflare API format for synchronization

#### Retention Policy
- Represents how long a tunnel route should persist after container stops
- Attributes: policy type (immediate/duration/forever), timestamp, container ID
- Evaluated by garbage collection process

#### Origin Request Configuration
- Represents connection and security settings for origin communication
- Attributes: TLS verify flag, timeouts, keep-alive settings, HTTP/2 enablement, Access policies, proxy settings
- Merged from global defaults and container-specific labels

#### State Snapshot
- Represents the persisted state of the controller for recovery after restart
- Attributes: timestamp, active tunnel entries map, pending deletions map, flapping container state
- Serialized to local file on configurable intervals

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Users can start a Docker container with appropriate labels and access it via Cloudflare Tunnel within 10 seconds of container start (including 5-second allowance for DNS propagation)
- **SC-002**: System handles 100 containers starting simultaneously without configuration errors or data loss
- **SC-003**: System processes container stop events and updates Cloudflare configuration within 5 seconds for immediate retention policy
- **SC-004**: 95% of users successfully expose their first container through Cloudflare Tunnel on first attempt without manual configuration beyond labels
- **SC-005**: System maintains correct Cloudflare configuration state with 99.9% accuracy over 24-hour period with no configuration drift
- **SC-006**: Container restarts (within retention period) result in zero service interruption for end users accessing through Cloudflare Tunnel
- **SC-007**: System recovers from temporary failures (Cloudflare API timeouts, Docker daemon restarts) without data loss or incorrect tunnel states
- **SC-008**: Configuration with Traefik labels works correctly for 100% of standard Traefik router and service label patterns
- **SC-009**: System uses no more than 100MB of RAM when managing 100 containers
- **SC-010**: Cloudflare API rate limit errors result in automatic retries that succeed within 30 seconds for 99% of cases
- **SC-011**: Users can migrate from Traefik to DockTunnel by changing 0 labels (Traefik labels work out of the box) for basic hostname and port configurations
- **SC-012**: System supports at least 3 different retention policies (immediate, timed, forever) across different containers simultaneously

## Assumptions

1. **Cloudflare Tunnel Pre-existence**: A Cloudflare Tunnel has already been created and configured in the user's Cloudflare account. The controller manages tunnel ingress rules but does not create the tunnel itself.

2. **Docker Socket Access**: The controller has access to `/var/run/docker.sock` either running on the host or with appropriate volume mounting when running as a container.

3. **Container Network Exposure**: Containers expose at least one port or define a service URL. Containers without any exposed ports cannot be routed through Cloudflare Tunnel.

4. **Cloudflare API Permissions**: The provided API token has sufficient permissions for Account (Read/Write), Zone (Read/Write), and Tunnel (Read/Write) operations.

5. **Single Tunnel Management**: The controller manages a single Cloudflare Tunnel per instance. Users requiring multiple tunnels should run multiple controller instances.

6. **Bridge or Host Networking**: Containers use either bridge networking (default Docker) or host networking. Other network drivers (macvlan, overlay) are not supported in the initial implementation.

7. **Standard Docker Labels**: Container labels follow Docker's standard label format (key-value string pairs). Complex nested configuration is flattened using dot notation.

8. **State Persistence Filesystem**: The controller has write access to a local filesystem for persisting state (typically mounted as a volume when running in a container).

9. **Clock Synchronization**: Container host and controller system have synchronized clocks (within NTP tolerance) for accurate retention timer calculations.

10. **Traefik Label Compatibility**: Traefik labels follow the standard v2 format (`traefik.http.routers.<name>.rule` and `traefik.http.services.<name>.loadbalancer.server.port`). Custom or deprecated label formats are not supported.

11. **Global Default Configuration**: Users will configure global defaults (timeouts, TLS settings) in a config.yaml file or via environment variables. The system provides sensible defaults for all settings.

12. **Single Controller Instance**: Only one instance of the controller runs per Docker host at a time. Running multiple instances on the same host with access to the same Docker socket is not supported.

13. **No Service Discovery**: The controller does not integrate with external service discovery mechanisms (Consul, etcd). It relies solely on Docker container state.
