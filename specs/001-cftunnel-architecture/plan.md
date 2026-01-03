# Implementation Plan: Cloudflare Tunnel Docker Controller

**Branch**: `001-cftunnel-architecture` | **Date**: 2026-01-03 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/001-cftunnel-architecture/spec.md`

## Summary

Build an event-driven Docker controller that automatically manages Cloudflare Tunnel ingress routes based on container labels. The system monitors Docker daemon events (container start/stop/die), parses container labels using a 4-layer priority hierarchy (DockTunnel → Traefik → auto-detection → global defaults), and synchronizes tunnel configurations with Cloudflare API. The architecture emphasizes reliability through continuous state reconciliation, fault tolerance via exponential backoff retries, and Traefik compatibility for zero-effort migration.

## Technical Context

**Language/Version**: Go 1.24
**Primary Dependencies**:
- `github.com/docker/docker/v28` - Docker daemon communication
- `github.com/cloudflare/cloudflare-go/v5` - Cloudflare API interaction
- `github.com/spf13/viper` - Configuration management
- `golang.org/x/time/rate` - Token bucket rate limiting
- `log/slog` (Go 1.21+ stdlib) - Structured logging

**Storage**:
- In-memory state maps (active tunnels, pending deletions, flapping detection)
- Local file persistence for state snapshots (JSON/binary gob encoding)
- No external database required

**Testing**:
- `go test` (standard Go testing)
- `github.com/stretchr/testify` - Assertion library
- Mock implementations for Cloudflare API (no real API calls in tests)
- Integration tests with testcontainers/Docker Test API

**Target Platform**: Linux server (Docker host or containerized)
**Project Type**: Single backend service (CLI daemon)
**Performance Goals**:
- Event processing: < 5 seconds from container start to tunnel creation
- Reconciliation: Every 30 seconds or on container event
- API rate limiting: 10 req/s (token bucket)
- Memory footprint: < 100MB for 100 containers

**Constraints**:
- Single Cloudflare Tunnel per controller instance
- Docker socket access required (`/var/run/docker.sock`)
- Bridge or host networking only (macvlan, overlay unsupported)
- Clock synchronization required (NTP tolerance) for retention timers

**Scale/Scope**:
- Support 100+ containers simultaneously
- 6 container services per container (multiple labels)
- 48 functional requirements across 8 modules
- 7 core entities with state transitions

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Pre-Design Compliance

✅ **I. Event-Driven Reliability**
- FR-001, FR-002: Event monitoring within 5 seconds
- FR-021, FR-023: State reconciliation every 30s
- FR-024: Debouncing for rapid changes
- FR-029: Exponential backoff retry (up to 3 times)
- FR-047: Graceful shutdown

✅ **II. Configuration Priority Fallback**
- FR-006 to FR-010: Explicit 4-layer hierarchy
- User Story 4: Auto-detection behavior
- FR-007: Traefik v2 label parsing

✅ **III. State Reconciliation & Fault Tolerance**
- FR-021 to FR-024: State management
- FR-045, FR-046: Flapping detection (60s window, 300s cooling)
- FR-029: API retry logic
- User Story 5: State persistence

✅ **IV. Validation & Uniqueness Constraints**
- FR-004, FR-005: Required field and hostname validation
- Edge Case #2: Duplicate hostname rejection
- FR-040: Label validation on startup

✅ **V. Observability & Debuggability**
- FR-037, FR-038: Structured logging (INFO/ERROR levels)
- SC-009: Memory footprint < 100MB
- FR-030: Rate limiting for API calls

✅ **VI. Traefik Compatibility**
- FR-007, FR-014: Traefik label parsing
- User Story 2: Zero-effort migration
- Edge Case handling for unsupported features

**Result**: ✅ **ALL GATES PASSED** - No violations requiring justification

## Project Structure

### Documentation (this feature)

```text
specs/001-cftunnel-architecture/
├── spec.md              # Feature specification
├── plan.md              # This file (implementation plan)
├── research.md          # Phase 0: Technical research and decisions
├── data-model.md        # Phase 1: Entity definitions and state transitions
├── quickstart.md        # Phase 1: Developer quickstart guide
├── contracts/           # Phase 1: External integration contracts
│   ├── cloudflare-api.md    # Cloudflare API interaction contract
│   ├── docker-events.md     # Docker event handling contract
│   └── label-schema.md      # Container label schema contract
└── tasks.md             # Phase 2: Task breakdown (created by /speckit.tasks)
```

### Source Code (repository root)

```text
cmd/
├── docktunnel/
│   └── main.go                 # Entry point, initialization, signal handling

internal/
├── config/
│   ├── config.go               # Configuration loading (Viper, YAML, env vars)
│   └── config_test.go
│
├── docker/
│   ├── monitor.go              # Docker event listener (goroutines)
│   ├── client.go               # Docker API client wrapper
│   └── monitor_test.go
│
├── cloudflareManager/
│   ├── tunnel.go               # Cloudflare API client, ingress rule management
│   ├── tunnel_test.go
│   ├── rate_limiter.go         # Token bucket rate limiting
│   └── retry.go                # Exponential backoff retry logic
│
├── controller/
│   ├── controller.go           # Core business logic, event orchestration
│   ├── label_parser.go         # 4-layer configuration resolution
│   ├── validator.go            # Hostname uniqueness, required fields
│   ├── controller_test.go
│   ├── label_parser_test.go
│   └── validator_test.go
│
├── state/
│   ├── manager.go              # State management, reconciliation loop
│   ├── persistence.go          # State snapshot save/load
│   └── manager_test.go
│
├── events/
│   ├── event.go                # Unified event structures
│   └── dispatcher.go           # Event channel distribution
│
└── logger/
    ├── logger.go               # Structured logging wrapper (log/slog)
    └── logger_test.go

pkg/
└── types/
    └── tunnel.go               # Public types (TunnelEntry, IngressRule, etc.)

tests/
├── integration/
│   ├── docker_events_test.go   # Real Docker daemon event handling
│   └── end_to_end_test.go      # Full container lifecycle integration
├── mocks/
│   ├── cloudflare_api.go       # Mock Cloudflare client
│   └── docker_client.go        # Mock Docker client
└── testhelpers/
    └── container.go            # Test container builders

config.yaml                     # Default configuration template
README.md                       # User documentation
Makefile                        # Build, test, run targets
Dockerfile                      # Container image build
go.mod, go.sum                  # Go module definition
```

**Structure Decision**: Single project structure chosen because this is a single-purpose backend service (CLI daemon) with no frontend or mobile components. The `internal/` package structure follows Go best practices with clear separation of concerns: configuration, Docker integration, Cloudflare integration, business logic, state management, and logging. Public types in `pkg/types/` allow external tooling integration if needed. Comprehensive test coverage at unit, integration, and contract levels.

## Complexity Tracking

> No constitution violations requiring justification

All features align with the 6 core principles. No additional complexity beyond standard event-driven architecture patterns.

