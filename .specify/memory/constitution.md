<!--
Sync Impact Report:
- Version change: None → 1.0.0
- Modified principles: N/A (initial creation)
- Added sections: All sections (initial creation)
- Removed sections: None
- Templates requiring updates:
  ✅ .specify/templates/plan-template.md (reviewed, Constitution Check section exists)
  ✅ .specify/templates/spec-template.md (reviewed, aligns with event-driven architecture)
  ✅ .specify/templates/tasks-template.md (reviewed, task categories align)
  ⚠ .specify/templates/commands/*.md (review pending - verify no hardcoded assumptions)
- Follow-up TODOs: None
-->

# DockTunnel Constitution

## Core Principles

### I. Event-Driven Reliability (NON-NEGOTIABLE)

The system MUST process all Docker container events (start, stop, die) within 5 seconds and maintain synchronization with Cloudflare Tunnel state.

- **Event Processing MUST** be idempotent - processing the same event multiple times produces identical results
- **Event Ordering MUST** be preserved - if container A starts before container B, A's configuration must be applied first
- **Event Loss MUST NOT** occur - all events with `docktunnel.enable=true` MUST be tracked until successfully synchronized
- **Failure Handling MUST** isolate errors - one container's configuration failure MUST NOT prevent processing other containers

**Rationale**: DockTunnel is a critical infrastructure component. Event loss or ordering violations cause configuration drift, leading to service unavailability or stale routes. The 5-second SLA balances responsiveness with API rate limits while ensuring rapid service exposure.

### II. Configuration Priority Fallback

Configuration MUST follow a strict 4-layer priority hierarchy: DockTunnel labels → Traefik labels → Auto-detection → Global defaults.

- **DockTunnel Labels** (`docktunnel.<name>.<attribute>`) MUST take precedence over all other sources
- **Traefik Compatibility** MUST be maintained for standard v2 labels (`traefik.http.routers.*`, `traefik.http.services.*`)
- **Auto-Detection** MUST fill gaps by inspecting container network settings and exposed ports
- **Global Defaults** MUST provide sensible fallbacks for all optional settings
- **Explicit Configuration** MUST always override implicit detection at each priority layer

**Rationale**: Users bring diverse deployment patterns (from Traefik migration to greenfield). The 4-layer hierarchy ensures maximum compatibility (Traefik users need zero changes) while enabling minimal configuration for new users. Layered overrides provide predictability - users always know which configuration source wins.

### III. State Reconciliation & Fault Tolerance

The system MUST maintain an authoritative "desired state" and continuously reconcile against actual Cloudflare configuration.

- **State Source of Truth** is the running Docker containers, not Cloudflare configuration
- **Reconciliation MUST** occur at least every 30 seconds OR on any container event
- **Controller Restarts MUST** trigger full container scan to detect changes during downtime
- **Flapping Detection MUST** implement exponential backoff (default 60s window, 300s cooling period)
- **API Failures MUST** retry with exponential backoff up to 3 times before failing
- **Graceful Shutdown MUST** complete in-flight synchronization operations

**Rationale**: Distributed systems experience partial failures (network blips, API rate limits, controller crashes). Continuous reconciliation ensures self-healing - the system eventually converges to correct state without manual intervention. State derived from Docker (not Cloudflare) ensures containers are the authoritative source.

### IV. Validation & Uniqueness Constraints

All configuration MUST be validated before application, with hostname uniqueness enforced globally across all containers.

- **Hostname Uniqueness** MUST be validated across ALL containers (not just within a single container)
- **Duplicate Hostnames MUST** be rejected with a clear error message identifying the conflicting containers
- **Required Fields** (hostname, service URL) MUST be present before creating tunnel rules
- **Service URLs** MUST be validated for correct format and network reachability
- **Label Parsing Errors** MUST NOT crash the controller - log errors and skip the container
- **Validation Failures** MUST be logged with sufficient context for troubleshooting

**Rationale**: Cloudflare Tunnel allows only one route per hostname. Duplicate hostnames cause "last write wins" behavior, leading to traffic misrouting. Pre-validation prevents API pollution and provides clear user feedback. Continuing on non-fatal errors ensures one misconfigured container doesn't block the entire system.

### V. Observability & Debuggability

All system behavior MUST be observable through structured logging with configurable verbosity levels.

- **Configuration Changes** MUST be logged at INFO level with before/after state
- **Errors** MUST be logged at ERROR level with full context (container ID, labels, error details)
- **Debug Information** (container inspection, API requests/responses) MUST be available at DEBUG level
- **Structured Logging** MUST use Go's `log/slog` with consistent key-value pairs
- **Log Formats** MUST support both human-readable (text) and machine-parsable (JSON) output
- **Performance Metrics** (event processing time, API call duration) MUST be logged

**Rationale**: Infrastructure tools run unattended. When issues arise, operators need detailed logs to diagnose problems without reproducing them. Structured logs enable log aggregation and alerting. Multiple formats support both human operators (during development) and automated systems (production monitoring).

### VI. Traefik Compatibility (Migration Support)

The system MUST parse standard Traefik v2 labels as a fallback configuration source, enabling zero-effort migration.

- **Traefik Router Rules** (`traefik.http.routers.<name>.rule`) MUST be parsed with regex to extract `Host()` patterns
- **Traefik Service Ports** (`traefik.http.services.<name>.loadbalancer.server.port`) MUST be extracted when specified
- **Service Name Matching** MUST link Traefik routers to services via the `<name>` identifier
- **Traefik Labels MUST** have lower priority than DockTunnel labels (DockTunnel always wins if both present)
- **Unsupported Traefik Features** (middleware, TLS config) MUST be logged as INFO-level warnings, not errors
- **Standard Traefik v2 Format** MUST be supported; custom or deprecated formats are out of scope

**Rationale**: Many potential users already run Traefik. Requiring label rewrites creates adoption friction. Traefik compatibility lowers switching costs to near-zero. Prioritizing DockTunnel labels ensures users can override Traefik-derived config when needed. Logging unsupported features educates users without blocking migration.

## Quality Standards

### Testing Discipline

- **Unit Tests** MUST exist for all label parsing logic, validation rules, and state reconciliation algorithms
- **Integration Tests** MUST verify Docker event handling with real container lifecycle
- **Mock-Based Tests** MUST be used for Cloudflare API interactions (no real API calls in test suite)
- **Flapping Detection Logic** MUST have dedicated tests covering rapid start/stop scenarios
- **Configuration Priority** MUST be tested with all 4 layers to ensure correct override behavior
- **Edge Cases** (no exposed ports, host networking, malformed labels) MUST have test coverage

### Performance & Resource Constraints

- **Memory Footprint** MUST NOT exceed 100MB when managing 100 containers
- **Event Processing Latency** (container start → route creation) MUST complete within 5 seconds under normal load
- **API Rate Limiting** MUST respect Cloudflare limits (default 10 requests/second) using token bucket algorithm
- **Concurrent Container Starts** (100 simultaneous containers) MUST be handled without event loss or race conditions
- **State File Size** MUST remain proportional to managed containers (no unbounded growth)
- **Garbage Collection Overhead** MUST be minimal (background ticker every 60 seconds)

### Security & Configuration Management

- **API Tokens** MUST be loaded from environment variables or configuration files, never hardcoded
- **Docker Socket Access** MUST be clearly documented as a security requirement in deployment docs
- **Configuration Validation** MUST occur at startup (fail fast if required fields missing)
- **Secrets Logging** MUST be prevented - API tokens and sensitive credentials MUST never appear in logs
- **Label Injection Attacks** MUST be prevented by validating all user-supplied labels before use
- **State File Permissions** MUST be restrictive (read/write for controller process only)

## Development Workflow

### Code Review Requirements

- **All Pull Requests** MUST include tests for new functionality
- **Label Parser Changes** MUST include test cases covering all 4 priority layers
- **State Management Changes** MUST include tests for flapping scenarios and controller restarts
- **Cloudflare API Integration Changes** MUST update mocks and test error paths
- **Validation Logic Changes** MUST include tests for both success and failure cases

### Compliance Verification

- **Constitution Compliance** MUST be verified for all PRs (automated checks where possible)
- **Hostname Uniqueness** MUST be enforced in code and tested
- **Event-Driven Guarantees** (idempotency, ordering) MUST have test coverage
- **Logging Statements** MUST use structured logging with appropriate levels
- **Configuration Examples** in documentation MUST be tested to ensure they work as documented

### Amendment Procedure

1. **Proposal**: Document proposed change with rationale and impact analysis
2. **Review**: Team reviews for compatibility with existing principles and architecture
3. **Version Bump**: Update `CONSTITUTION_VERSION` following semantic versioning:
   - MAJOR: Principle removal or backward-incompatible governance changes
   - MINOR: New principle added or existing principle materially expanded
   - PATCH: Clarifications, wording improvements, non-semantic changes
4. **Template Sync**: Update dependent templates (plan, spec, tasks) to reflect new principles
5. **Migration Plan**: For MAJOR/MINOR changes, document migration path for existing features

## Governance

This constitution governs all DockTunnel development. Feature specifications, implementation plans, and code changes MUST comply with these principles.

- **Principle Violations** require explicit justification and documented exception approval
- **Ambiguities** SHOULD be clarified via amendments rather than ad-hoc interpretation
- **Runtime Development Guidance** is available in `CLAUDE.md` for agent-assisted development
- **Compliance Reviews** occur during feature planning (`/speckit.plan`) and code review
- **Constitution Supersedes** conflicting practices in other documentation or team conventions

**Version**: 1.0.0 | **Ratified**: 2026-01-03 | **Last Amended**: 2026-01-03
