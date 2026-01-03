# Technical Research & Decisions

**Feature**: Cloudflare Tunnel Docker Controller
**Date**: 2026-01-03
**Phase**: 0 - Research & Technology Decisions

## Overview

This document captures technical research findings and architectural decisions for the DockTunnel controller. All decisions align with the constitution's 6 core principles and support the 48 functional requirements.

## Decision Record

### D1: Go Version Selection

**Decision**: Go 1.24

**Rationale**:
- `log/slog` for structured logging (Constitution Principle V)
- Native support for time-rate limiting in `golang.org/x/time/rate`
- Strong concurrency model (goroutines) for event-driven architecture
- Single binary deployment simplifies operations
- Excellent Docker SDK and Cloudflare SDK support

**Alternatives Considered**:
- Python 3.11: Rejected due to async complexity with event streams
- Rust 1.75: Rejected due to Docker SDK maturity concerns
- Node.js 20: Rejected due to weaker type safety for configuration parsing

**Alignment**: Constitution Principle I (Event-Driven Reliability) - goroutines + channels provide natural event ordering

---

### D2: State Persistence Mechanism

**Decision**: File-based persistence with gob encoding + JSON fallback

**Rationale**:
- No external database dependency (simplifies deployment)
- Fast serialization for in-memory map structures
- Human-readable JSON for debugging (fallback)
- Atomic file writes prevent corruption
- Meets FR-020: persist retention state across restarts

**Alternatives Considered**:
- SQLite: Rejected - overkill for 2-3 maps
- Redis: Rejected - adds operational dependency
- etcd: Rejected - too complex for single-host use

**Implementation**:
```go
// Primary: gob encoding (binary, fast)
// Fallback: JSON (human-readable debug)
encoder := gob.NewEncoder(file)
err := encoder.Encode(stateSnapshot)

// Atomic write pattern
tmpFile := fmt.Sprintf("%s.tmp", statePath)
os.Rename(tmpFile, statePath)
```

**Alignment**: Constitution Principle III (State Reconciliation) - enables recovery from controller crashes

---

### D3: Event Processing Architecture

**Decision**: Single-producer multi-consumer pattern with buffered channels

**Rationale**:
- Docker event stream → single goroutine producer
- Worker pool (configurable, default 5) for event processing
- Buffered channel (1000 events) prevents backpressure
- Preserves event ordering (Constitution Principle I)
- Enables graceful shutdown via channel closing

**Architecture**:
```
Docker Events → EventChannel → Dispatcher → Worker Pool → State Updates
                                                  ↓
                                          Cloudflare API Sync
```

**Alternatives Considered**:
- Fan-out to multiple channels: Rejected - adds complexity without benefit
- Unbuffered channels: Rejected - blocks Docker event stream
- Direct processing: Rejected - violates ordering guarantees

**Alignment**: Constitution Principle I (Event Ordering) - single channel ensures FIFO processing

---

### D4: Cloudflare API Rate Limiting Strategy

**Decision**: Token bucket algorithm with 10 req/s limit and configurable burst

**Rationale**:
- Matches Cloudflare's documented rate limits
- Token bucket allows short bursts for batch operations
- Prevents API throttling during container starts
- Implements FR-030

**Implementation**:
```go
import "golang.org/x/time/rate"

limiter := rate.NewLimiter(10, 20) // 10 req/s, burst of 20
err := limiter.Wait(ctx) // Blocks until token available
```

**Alternatives Considered**:
- Fixed window counter: Rejected - allows thundering herd
- Sliding window log: Rejected - higher memory overhead
- No rate limiting: Rejected - violates Cloudflare ToS

**Alignment**: Constitution Principle V (Observability) - log rate limit events at WARN level

---

### D5: Traefik Label Parsing Approach

**Decision**: Regex-based extraction with `Host()` and `Path()` pattern matching

**Rationale**:
- Traefik v2 uses `Host(`example.com`)` syntax in router rules
- Standard regex: `Host\(['"]([^'"]+)['"]\)`
- Supports both single and multiple hostnames
- Logs unsupported features as INFO (Constitution Principle VI)
- Implements FR-014

**Pattern Examples**:
```
traefik.http.routers.myapp.rule=Host(`example.com`) && PathPrefix(`/api`)
→ Extracts: hostname=example.com, path=/api

traefik.http.routers.web.rule=Host(`web.com`, `www.web.com`)
→ Extracts: hostnames=[web.com, www.web.com] (creates multiple routes)
```

**Alternatives Considered**:
- Traefik library import: Rejected - tight coupling to Traefik internals
- Custom parser: Rejected - reinventing well-tested regex
- Full expression parser: Rejected - overkill for Host/Path extraction

**Alignment**: Constitution Principle VI (Traefik Compatibility) - zero-effort migration path

---

### D6: Container IP Detection Strategy

**Decision**: Multi-stage fallback with network mode detection

**Rationale**:
- Host networking: Use `localhost` (FR-012)
- Bridge networking: Use first network's IPAddress (FR-013)
- Validates reachability via Docker API
- Implements FR-011

**Algorithm**:
```go
func detectContainerIP(containerJSON *ContainerJSON) string {
    if containerJSON.HostConfig.NetworkMode == "host" {
        return "localhost"
    }

    // Bridge mode: use first network's IP
    for _, network := range containerJSON.NetworkSettings.Networks {
        if network.IPAddress != "" {
            return network.IPAddress
        }
    }

    return "" // Triggers validation error (FR-004)
}
```

**Alternatives Considered**:
- Always use container name: Rejected - requires Docker DNS
- Gateway IP: Rejected - incorrect routing
- First exposed port binding: Rejected - doesn't work for bridge

**Alignment**: Constitution Principle IV (Validation) - fails fast if IP cannot be determined

---

### D7: Debouncing Implementation

**Decision**: Time-window accumulator with 2-second default delay

**Rationale**:
- Batches rapid container state changes (FR-024)
- Reduces Cloudflare API calls during deployments
- Configurable window size
- Preserves final state only

**Behavior**:
```
Container starts at t=0 → event queued
Container stops at t=1 → event queued
Window expires at t=2 → process final state (stopped)
Result: Only one API call instead of two
```

**Alternatives Considered**:
- Immediate processing: Rejected - causes API thrashing
- Fixed delay: Rejected - doesn't adapt to event burst size
- Exponential backoff: Rejected - conflicts with flapping detection

**Alignment**: Constitution Principle I (Event Processing) - reduces API load while maintaining accuracy

---

### D8: Flapping Detection Algorithm

**Decision**: Sliding window counter with 60-second observation period

**Rationale**:
- Tracks state transitions (running ↔ stopped) in time window
- Exponential backoff: 300s cooling period after 5 transitions
- Implements FR-045, FR-046
- Prevents configuration thrashing

**Algorithm**:
```go
type FlappingDetector struct {
    transitions []time.Time
    window      time.Duration // 60s
    threshold   int           // 5 transitions
    cooling     time.Duration // 300s
}

func (f *FlappingDetector) IsFlapping(containerID string) bool {
    // Count transitions in last 60s
    // If > 5, mark for cooling period
}
```

**Alternatives Considered**:
- Fixed counter: Rejected - doesn't account for time distribution
- Rate limiter: Rejected - doesn't capture pattern
- Machine learning: Rejected - overkill for simple pattern

**Alignment**: Constitution Principle III (Fault Tolerance) - protects system from unstable containers

---

### D9: Configuration Loading Priority

**Decision**: Viper library with cascading priority (high to low):

1. Environment variables (`DOCKTUNNEL_*`)
2. Config file (`./config.yaml` → `/etc/docktunnel/config.yaml`)
3. Defaults (coded)

**Rationale**:
- Industry-standard practice (12-factor app)
- Implements FR-041, FR-042
- Environment variables override files for containers
- Dot notation maps to nested structs

**Example**:
```yaml
cloudflare:
  accountId: "abc123"
  apiToken: "xyz789"
```
```bash
export DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID="override123"
# Environment variable takes precedence
```

**Alternatives Considered**:
- Files only: Rejected - inflexible for containers
- Flags only: Rejected - verbose for many options
- Custom parser: Rejected - Viper is battle-tested

**Alignment**: Constitution Principle IV (Validation) - validates required fields on startup (FR-044)

---

### D10: Retry Strategy for Cloudflare API

**Decision**: Exponential backoff with jitter, max 3 retries

**Rationale**:
- Implements FR-029
- Handles transient failures (network blips, rate limits)
- Jitter prevents thundering herd
- Max 3 retries prevents infinite loops

**Backoff Schedule**:
```
Attempt 1: Immediate
Attempt 2: 1s + random(0-500ms)
Attempt 3: 2s + random(0-1000ms)
Attempt 4: 4s + random(0-2000ms) [fails after this]
```

**Implementation**:
```go
import "github.com/sethvargo/go-retry"

backoff := retry.NewExponential(1 * time.Second)
backoff = retry.WithMaxRetries(3, backoff)
backoff = retry.WithJitter(500*time.Millisecond, backoff)
```

**Alternatives Considered**:
- Fixed delay: Rejected - inefficient for rate limits
- No jitter: Rejected - causes synchronized retries
- Infinite retries: Rejected - blocks event processing

**Alignment**: Constitution Principle III (Fault Tolerance) - self-healing from transient failures

---

### D11: Testing Strategy

**Decision**: Three-tier testing pyramid with mocks for external dependencies

**Rationale**:
- Unit tests: Label parser, validators, state transitions
- Integration tests: Docker event handling, state persistence
- Contract tests: Mock Cloudflare API, Mock Docker client
- No real API calls in automated tests (security/cost)

**Test Coverage**:
```
internal/controller/
├── label_parser_test.go      # Unit: 4-layer resolution
├── validator_test.go         # Unit: hostname uniqueness
└── controller_test.go        # Integration: event processing

tests/integration/
├── docker_events_test.go     # Real Docker daemon
└── end_to_end_test.go        # Full lifecycle

tests/mocks/
├── cloudflare_api.go         # Interface-based mock
└── docker_client.go          # Interface-based mock
```

**Alternatives Considered**:
- Real Cloudflare API: Rejected - requires credentials, slow, costly
- Only unit tests: Rejected - misses integration bugs
- Manual testing only: Rejected - not reproducible

**Alignment**: Constitution Quality Standards (Testing Discipline) - mandatory unit/integration/mock tests

---

### D12: Graceful Shutdown Mechanism

**Decision**: Signal handler (SIGTERM, SIGINT) with context cancellation

**Rationale**:
- Implements FR-047
- Completes in-flight API operations
- Flushes state to disk
- Closes Docker event stream
- Sets 30-second timeout for shutdown

**Implementation**:
```go
ctx, cancel := context.WithCancel(context.Background())

// Handle signals
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

go func() {
    <-sigCh
    cancel() // Signal all goroutines to stop

    // Wait for in-flight operations
    shutdownCtx, _ := context.WithTimeout(context.Background(), 30*time.Second)
    waitForCompletion(shutdownCtx)

    // Persist state
    stateManager.Save()
    os.Exit(0)
}()
```

**Alternatives Considered**:
- Immediate exit: Rejected - loses state, corrupts API
- No timeout: Rejected - can hang indefinitely
- Deferred cleanup only: Rejected - doesn't complete in-flight work

**Alignment**: Constitution Principle I (Event-Driven Reliability) - no event loss during shutdown

---

## Unresolved Questions

None. All technical decisions resolved.

## References

- [Docker Engine API Documentation](https://docs.docker.com/engine/api/)
- [Cloudflare Tunnel API](https://developers.cloudflare.com/api/resources/cloudflare_tunnel/subresources/ingress/rules/)
- [Traefik v2 Label Documentation](https://doc.traefik.io/traefik/providers/docker/)
- [Go Rate Limiting](https://pkg.go.dev/golang.org/x/time/rate)
- [Viper Configuration Guide](https://github.com/spf13/viper)
