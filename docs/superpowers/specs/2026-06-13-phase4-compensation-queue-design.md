# Phase 4: Failure Compensation & Retry Governance

**Date:** 2026-06-13
**Status:** Approved
**Depends on:** Phase 3 (per-service compound keys, state machine transitions)

## Problem

When the controller executes transition actions (e.g., `ActionDeleteRoute`), a Cloudflare API failure causes the action to be logged and forgotten. This leaves the system in an inconsistent state — DNS records and ingress rules that should have been deleted remain active. The Cloudflare manager already retries individual API calls (3 attempts, exponential backoff), but once those retries are exhausted the failure is permanent and unrecoverable.

## Approach

Add a durable compensation queue inside the state manager. When an action execution fails, the controller enqueues a `CompensationRecord`. A background goroutine retries failed actions with exponential backoff. The queue is persisted in the gob state file, so records survive process restarts.

Scope is limited to `ActionDeleteRoute` only. Create-route operations are handled by the existing `syncToCloudflare` full-state-sync mechanism and don't need compensation (a missed create will be caught on the next container start or sync cycle).

---

## 1. Error Types

**File:** `pkg/types/errors.go` (new)

Two custom error types wrapping underlying errors. They allow the compensation system to decide retry vs abandon without re-inspecting HTTP status codes.

```go
type RetryableError struct {
    Err error
}
func (e *RetryableError) Error() string { return fmt.Sprintf("retryable: %v", e.Err) }
func (e *RetryableError) Unwrap() error { return e.Err }

type PermanentError struct {
    Err error
}
func (e *PermanentError) Error() string { return fmt.Sprintf("permanent: %v", e.Err) }
func (e *PermanentError) Unwrap() error { return e.Err }
```

**Cloudflare manager change:** When `callWithRetry` exhausts all retries, it wraps the final error:
- 429, 5xx, timeout, connection errors → `RetryableError`
- 401, 403, 4xx (bad params) → `PermanentError`

---

## 2. CompensationRecord Type

**File:** `pkg/types/tunnel.go` (addition)

```go
type CompensationRecord struct {
    ID          string        // unique ID (timestamp-based)
    Action      Action        // the action to retry
    RetryCount  int
    MaxRetries  int
    NextRetryAt time.Time
    LastError   string        // error message from last attempt
    CreatedAt   time.Time
    Dead        bool          // true if permanently failed or retries exhausted
}
```

---

## 3. State Manager Changes

**File:** `internal/state/manager.go`

**New fields on Manager:**

- `pendingActions map[string]*types.CompensationRecord` — keyed by record ID
- `compensateInterval time.Duration` — poll interval for background loop (default 30s)
- `compensationCfg types.CompensationConfig` — retry parameters

**New public methods:**

- `EnqueueAction(action Action, err error)` — classifies the error:
  - `PermanentError` → creates record with `Dead = true`
  - `RetryableError` → creates record with `NextRetryAt = now + InitialDelay`
  - Plain `error` → treats as retryable (conservative default)
- `RunCompensation(ctx context.Context, executor func(Action) error)` — background loop:
  - Wakes every `compensateInterval`
  - Processes records where `NextRetryAt <= now` and `Dead == false`
  - On success: removes record from queue
  - On retryable failure: increments `RetryCount`, calculates `NextRetryAt` with exponential backoff (`InitialDelay * 2^RetryCount`, capped at `MaxDelay`)
  - On permanent failure or `RetryCount >= MaxRetries`: marks `Dead = true`
- `GetDeadActions() []CompensationRecord` — returns permanently failed records for inspection

**Snapshot changes:**

- `GetSnapshot` includes `PendingActions`
- `LoadFromSnapshot` restores `pendingActions`
- `StateVersion` bumps from 2 → 3
- Migration: v2 snapshots get an empty `PendingActions` map
- `RegisterGobTypes` registers `CompensationRecord` and `map[string]*types.CompensationRecord`

---

## 4. Controller Integration

**File:** `internal/controller/controller.go`

**`handleContainerStop` change:**

```go
for _, action := range allActions {
    if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
        delete(c.ingressRules, action.Hostname)
        if err := c.syncToCloudflare(); err != nil {
            c.stateManager.EnqueueAction(action, err)
        }
    }
}
```

**Background goroutine** (started in `main.go` alongside existing GC goroutine):

```go
go func() {
    executor := func(action types.Action) error {
        return c.executeAction(action)
    }
    sm.RunCompensation(ctx, executor)
}()
```

`executeAction` is a new method on the controller that encapsulates the action execution logic: remove the hostname from the local ingress rules map, then call `syncToCloudflare` to push the updated configuration. This is the same logic currently inlined in `handleContainerStop`, factored out for reuse by the compensation loop.

---

## 5. Configuration

**File:** `internal/config/config.go`

```go
type CompensationConfig struct {
    InitialDelay  time.Duration
    MaxDelay      time.Duration
    MaxRetries    int
    PollInterval  time.Duration
}
```

**YAML example:**

```yaml
compensation:
  initialDelay: 30s
  maxDelay: 30m
  maxRetries: 10
  pollInterval: 30s
```

**Defaults** (used when config is zero-valued):

| Field | Default | Rationale |
|-------|---------|-----------|
| InitialDelay | 30s | Long enough for transient CF outages to resolve |
| MaxDelay | 30m | Cap prevents absurd wait times |
| MaxRetries | 10 | ~5.5 hours total retry window |
| PollInterval | 30s | Balances responsiveness vs overhead |

Existing deployments without the `compensation` section silently get default behavior.

---

## 6. State File Version Migration

- **v2 → v3:** Add `PendingActions` field (empty map for v2 snapshots)
- Backward-compatible: `StateVersion` validation uses `snapshot.Version > StateVersion` (already in place from Phase 3)

---

## Out of Scope

- `ActionCreateRoute` compensation (handled by sync mechanism)
- Circuit breaker pattern
- Metrics/monitoring endpoints
- Dead letter queue alerting (dead records are logged, no notification mechanism)
