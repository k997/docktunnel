# Docker Reconnection & Health Status Monitoring

**Date**: 2026-06-12
**Status**: Approved
**Inspired by**: Traefik Docker provider (`pkg/provider/docker/pdocker.go`)

## Background

DockTunnel monitors Docker container events to manage Cloudflare Tunnel configuration.
Compared to Traefik's Docker provider, DockTunnel is missing two key capabilities:

1. **No auto-reconnection** — when the Docker daemon disconnects, `ListenForEvents()` returns an error and the event goroutine exits permanently
2. **No health status monitoring** — DockTunnel doesn't listen to `health_status` Docker events, so unhealthy containers remain exposed

DockTunnel already surpasses Traefik in: incremental event processing, debouncing, and flapping detection.

## Design

### 1. Auto-Reconnection with Exponential Backoff

**File**: `internal/docker/monitor.go`

Refactor `ListenForEvents()` into a reconnection loop:

- Extract current event listening logic into `listenOnce(ctx, eventCh) error`
- Wrap in a `for` loop with exponential backoff (initial: 1s, max: 60s, multiplier: 2x)
- On `listenOnce()` error (excluding context cancellation), log warning and wait for backoff period
- On successful connection, reset backoff to 1s
- After reconnection, call `ScanRunningContainers()` to reconcile state missed during disconnect

```
ListenForEvents():
    backoff = 1s
    loop:
        err = listenOnce()
        if err == nil or ctx cancelled:
            return
        log.warn("disconnected, reconnecting", backoff)
        sleep(backoff) or ctx cancel
        backoff = min(backoff * 2, 60s)
        on reconnect success:
            ScanRunningContainers()  // full state sync
            backoff = 1s
```

**Why self-implemented**: Avoids external dependency; the logic is simple enough (one loop, one timer).

### 2. Health Status Monitoring

**Strategy**: Containers WITH a Docker HEALTHCHECK are filtered by health status. Containers WITHOUT a HEALTHCHECK are always considered healthy.

#### 2.1 Event Types

**File**: `internal/events/event.go`

Add three new `ActionType` constants:

- `ActionHealthHealthy` (`"health_healthy"`)
- `ActionHealthUnhealthy` (`"health_unhealthy"`)
- `ActionHealthStarting` (`"health_starting"`)

#### 2.2 Event Listening

**File**: `internal/docker/monitor.go`

In `listenOnce()`, process `health_status` event actions from Docker:

- `"health_status: healthy"` → emit `ActionHealthHealthy`
- `"health_status: unhealthy"` → emit `ActionHealthUnhealthy`
- `"health_status: starting"` → emit `ActionHealthStarting`

These events are already filtered by `type=container` and `label=docktunnel.enable=true`.

#### 2.3 Controller Handling

**File**: `internal/controller/controller.go`

Add new cases in `Dispatch()`:

- `ActionHealthHealthy` → reuse label parsing and rule registration from `handleContainerStart`
- `ActionHealthUnhealthy` / `ActionHealthStarting` → reuse rule cleanup from `handleContainerStop`, but **skip flapping detection** (health state changes are not container restarts)

Health events flow through the same debounce pipeline as start/stop events.

### 3. Interaction with Existing Features

| Feature | Impact |
|---------|--------|
| **Debouncing** | Health events use existing debounce timer. No changes needed. |
| **Flapping detection** | Health events skip flapping check. Container restart events still trigger flapping as before. |
| **State persistence** | Health status is derived from Docker events at runtime. Not persisted — reconciled on reconnect via `ScanRunningContainers()`. |
| **DNS management** | Health-unhealthy triggers same DNS cleanup as container stop. Health-healthy triggers same DNS creation as container start. |

## Files Changed

| File | Change |
|------|--------|
| `internal/docker/monitor.go` | Refactor `ListenForEvents` into reconnection loop; add health_status event handling |
| `internal/events/event.go` | Add `ActionHealthHealthy`, `ActionHealthUnhealthy`, `ActionHealthStarting` |
| `internal/controller/controller.go` | Add health event dispatch cases; extract shared logic from start/stop handlers |

## Testing

### Unit Tests

1. **Reconnection logic** (`internal/docker/monitor_test.go`):
   - Mock Docker client returning errors → verify backoff increases and retry occurs
   - Mock successful reconnect → verify backoff resets and `ScanRunningContainers` is called
   - Context cancellation → verify clean exit without retry

2. **Health event processing** (`internal/controller/controller_test.go`):
   - `ActionHealthHealthy` → service exposed
   - `ActionHealthUnhealthy` → service removed
   - Container without HEALTHCHECK → no health events generated (default healthy)
   - Health events do not trigger flapping detection

3. **Event types** (`internal/events/event_test.go`):
   - New ActionType constants serialize/deserialize correctly

### Integration Tests

- Docker daemon disconnect/reconnect → DockTunnel recovers and syncs state
- Container with HEALTHCHECK → service only exposed when healthy
