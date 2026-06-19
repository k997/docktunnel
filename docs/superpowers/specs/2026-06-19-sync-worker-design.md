# Sync Worker: Serializing Cloudflare Writes

**Date:** 2026-06-19
**Status:** Approved
**Depends on:** Nothing (standalone refactor on top of existing debounce infrastructure)

## Problem

`Controller.performSync` writes the full desired state to Cloudflare (ingress config + DNS). Five call sites trigger it today: `handleContainerStart`, `handleContainerStop`, `handleResync`, `ExecuteAction` (compensation), and `Reconcile`. All five go through `syncToCloudflare`, which uses a `time.AfterFunc` to debounce and then spawns `go c.performSync(context.Background())` in a fresh goroutine.

A sixth site, `CleanupResources`, bypasses debounce and calls `performSync(ctx)` synchronously on the shutdown path.

This produces three concrete problems:

1. **Concurrent `performSync` invocations.** Debounce timer fires in goroutine A; simultaneously a second event arrives and the new timer fires in goroutine B before A finishes. Both goroutines call `UpdateConfiguration` and `syncDNSRecords` against the same tunnel with overlapping (possibly stale) rule sets.
2. **No dirty/coalesce semantics.** `pendingUpdates` is set/cleared but never read to skip redundant syncs. A 5-second sync with 100 inbound events schedules ~100 goroutines that all read the same final state.
3. **`performSync` doesn't hold `c.mu` during the CF writes**, so it reads `ingressRules` while another `syncToCloudflare` mutates it. The race is currently bounded by the fact that `GetIngressRules()` takes a brief RLock, but the surface is fragile.

## Scope

In scope:
- Introduce a `syncWorker` type that serializes all `performSync` calls.
- All five event-side callers go through `TriggerSync` (non-blocking, coalesced).
- `CleanupResources` goes through `FlushSync` (blocking until pending sync completes).
- Worker absorbs the existing debounce timer.

Out of scope (deferred to later phases):
- Introducing a typed `DesiredState` struct. The existing `ingressRules` map remains the desired-state representation.
- Per-resource state machine (DNS / Route decoupling).
- Sync retry logic. Failures are logged; reconciliation + compensation queue still drive convergence.
- Worker-state Prometheus metric. YAGNI for this phase.

## Non-Goals

- **Not changing** the existing `syncToCloudflare` function signature. It becomes a thin wrapper around `TriggerSync` so the five event-side callers don't need edits.
- **Not changing** error semantics of `performSync`. Errors are still returned to the caller (now: the worker), logged, and surfaced via metrics.
- **Not parallelizing** syncs. CF tunnel config is a single document; concurrent writes have no benefit.

---

## 1. Architecture

```
┌────────────────────────────────────────────────────────────┐
│ Controller                                                 │
│                                                            │
│  ┌──────────────┐         ┌──────────────────────────┐     │
│  │ event path   │         │ compensation / cleanup   │     │
│  │ handlers     │         │ paths                    │     │
│  └──────┬───────┘         └──────────┬───────────────┘     │
│         │                            │                      │
│         │ syncToCloudflare()         │ CleanupResources()   │
│         │ → TriggerSync()            │ → FlushSync(ctx)     │
│         └────────────┬───────────────┘                      │
│                      ↓                                      │
│         ┌──────────────────────────┐                       │
│         │  syncWorker (unexported) │                       │
│         │  ──────────────────────  │                       │
│         │  debounce: 2s            │                       │
│         │  triggerCh: chan{} (1)   │                       │
│         │  flushQueue: chan chan{} │                       │
│         │  stopCh: chan{}          │                       │
│         │  lastErr: atomic.Pointer[error] │                │
│         │  ──────────────────────  │                       │
│         │  syncFn = performSync    │                       │
│         └────────────┬─────────────┘                       │
│                      │                                      │
└──────────────────────┼──────────────────────────────────────┘
                       │
                       ↓
              single goroutine: run()
                       │
                       ↓
        serial performSync(ctx) calls (no concurrency)
```

**Ownership and lifecycle:**
- `syncWorker` is an unexported type in `internal/controller`.
- `Controller` holds `syncWorker *syncWorker`, constructed in `NewController`.
- Channels exist after construction; only the goroutine is started later.
- `Start(ctx)` launches the goroutine. Called from `main.go` alongside `RunCompensationLoop`.
- Worker exits when `ctx` is cancelled. `stopCh` is closed on exit for shutdown synchronization.

## 2. Data Flow

### 2.1 Normal trigger path (event handlers)

```
handleContainerStart()
  → syncToCloudflare()
     → c.syncWorker.TriggerSync()
        → non-blocking send to triggerCh (size 1)
        → return immediately
```

### 2.2 Worker loop

```
state = idle
loop:
  <-triggerCh                    // block until triggered
  timer.Reset(debounce)          // wait 2s to coalesce
  drain_loop:
    select {
      case <-triggerCh:          // another trigger arrived during debounce
           timer.Reset(debounce) // restart debounce window
      case <-timer.C:            // debounce elapsed
           break drain_loop
      case <-ctx.Done():
           return
    }
  call syncFn(ctx) with panic recovery
  drain pending flushQueue entries (close each done channel)
  // re-check triggerCh without blocking — if dirty, loop again
  select {
    case <-triggerCh:
         continue                // sync arrived during syncFn; run again
    default:
         // back to idle (outer loop)
  }
```

**Coalescing invariant:** `triggerCh` has size 1. If 100 triggers arrive while the channel is full, they're effectively dropped. This is correct because:
- Worker always re-checks `triggerCh` non-blocking after each `syncFn` completes.
- Any non-blocking receive consumes one pending signal; if the channel had anything in it, that counts as "at least one trigger happened since syncFn started".
- A dropped trigger is observationally equivalent to a coalesced one: both result in exactly one extra `syncFn` invocation reading the latest `ingressRules`.

### 2.3 Flush path (CleanupResources)

```
CleanupResources(ctx)
  → c.mu.Lock(); clear ingressRules + containerRules; c.mu.Unlock()
  → c.syncWorker.FlushSync(ctx)
     → triggerCh <- struct{}{}                // make sure there's something to do
     → myDone := make(chan struct{})
     → flushQueue <- myDone                   // register wait
     → block on { myDone closed | ctx.Done() }
     → return lastErr (or ctx.Err())
```

Worker drains `flushQueue` after every `syncFn` call (regardless of success/failure), closing each pending `done` channel.

### 2.4 Locking discipline

| Operation | Lock |
|---|---|
| Event handler reads/writes `ingressRules`, `containerRules`, `containerHealth` | `c.mu` (write) / `c.mu.RLock` (read) — unchanged |
| `GetIngressRules()` inside `performSync` | `c.mu.RLock` — unchanged |
| Worker reading/writing its own state (`triggerCh`, `flushQueue`, `lastErr`) | No lock — single goroutine owns all of it; `lastErr` uses `atomic.Value` because `FlushSync` reads it from the caller's goroutine |
| `TriggerSync` / `FlushSync` called from event handlers | No `c.mu` needed — they only touch worker-owned channels |

## 3. API Surface

### 3.1 New file `internal/controller/sync_worker.go`

```go
package controller

import (
    "context"
    "log/slog"
    "sync/atomic"
    "time"
)

// syncWorker serializes Cloudflare writes via a single goroutine.
// All callers funnel through TriggerSync (async, debounced) or
// FlushSync (sync, waits for at least one sync cycle to complete).
type syncWorker struct {
    debounce   time.Duration
    syncFn     func(context.Context) error
    log        *slog.Logger

    triggerCh  chan struct{}        // size 1
    flushQueue chan chan struct{}   // unbuffered; FlushSync registers its done chan
    stopCh     chan struct{}        // closed when run() exits

    lastErr    atomic.Pointer[error] // last syncFn result; nil = success or never run
    started    atomic.Bool
}

func newSyncWorker(debounce time.Duration, syncFn func(context.Context) error, log *slog.Logger) *syncWorker {
    return &syncWorker{
        debounce:   debounce,
        syncFn:     syncFn,
        log:        log,
        triggerCh:  make(chan struct{}, 1),
        flushQueue: make(chan chan struct{}),
        stopCh:     make(chan struct{}),
    }
}

// Start launches the worker goroutine. Idempotent; returns silently if
// already started.
func (w *syncWorker) Start(ctx context.Context) {
    if !w.started.CompareAndSwap(false, true) {
        return
    }
    go w.run(ctx)
}

// TriggerSync signals that a sync is desired. Non-blocking; safe to call
// from event handlers. Multiple triggers coalesce.
func (w *syncWorker) TriggerSync() {
    select {
    case w.triggerCh <- struct{}{}:
    default:
        // channel full; worker will re-check after current sync completes
    }
}

// FlushSync triggers a sync and blocks until the worker has processed
// at least one sync cycle triggered by this call. Returns the last
// sync error (or nil) on completion, or ctx.Err() on timeout/cancel.
func (w *syncWorker) FlushSync(ctx context.Context) error {
    if !w.started.Load() {
        return fmt.Errorf("sync worker not started")
    }
    myDone := make(chan struct{})
    select {
    case w.triggerCh <- struct{}{}:
    case <-ctx.Done():
        return ctx.Err()
    default:
        // triggerCh full; worker will pick it up
    }
    select {
    case w.flushQueue <- myDone:
    case <-ctx.Done():
        return ctx.Err()
    }
    select {
    case <-myDone:
        if p := w.lastErr.Load(); p != nil {
            return *p
        }
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

// Stop blocks until the worker goroutine has exited. Used in tests
// and shutdown. Returns immediately if Start was never called.
func (w *syncWorker) Stop() {
    if !w.started.Load() {
        return
    }
    <-w.stopCh
}

func (w *syncWorker) run(ctx context.Context) {
    defer close(w.stopCh)
    timer := time.NewTimer(w.debounce)
    timer.Stop() // not yet armed
    defer timer.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-w.triggerCh:
            // debounce: wait for quiet period
            timer.Reset(w.debounce)
        debounce:
            for {
                select {
                case <-ctx.Done():
                    return
                case <-w.triggerCh:
                    timer.Reset(w.debounce)
                case <-timer.C:
                    break debounce
                }
            }
            w.runOnce(ctx)
        }
    }
}

func (w *syncWorker) runOnce(ctx context.Context) {
    defer func() {
        if r := recover(); r != nil {
            w.log.Error("syncFn panic recovered", "panic", r)
            w.lastErr.Store(fmt.Errorf("sync panicked: %v", r))
        }
        // Drain pending flushers regardless of outcome.
        for {
            select {
            case done := <-w.flushQueue:
                close(done)
            default:
                return
            }
        }
    }()
    err := w.syncFn(ctx)
    w.lastErr.Store(&err) // works for nil too: stores pointer-to-nil
    if err != nil {
        w.log.Warn("sync failed; will retry on next trigger", "error", err)
    }
}
```

### 3.2 `controller.go` changes

**Remove from `Controller` struct:**
- `debounceTimer *time.Timer`
- `pendingUpdates bool`

**Add:**
- `syncWorker *syncWorker`

**`NewController`:**
```go
// after defaults applied:
c.syncWorker = newSyncWorker(c.debounceDuration, c.performSync, slog.Default())
return c
```

**`syncToCloudflare` becomes:**
```go
func (c *Controller) syncToCloudflare(_ context.Context) error {
    c.syncWorker.TriggerSync()
    return nil
}
```
Signature unchanged (the `ctx` param is now unused — renamed to `_` to silence linters). Kept for API stability so the 8 call sites (controller.go lines 292, 436, 473, 658, 968, 1031, 1055, 1157) need zero edits.

**`CleanupResources` line 180:**
```go
// before:
if err := c.performSync(ctx); err != nil { ... }
// after:
if err := c.syncWorker.FlushSync(ctx); err != nil {
    slog.Error("Failed to perform cleanup sync", "error", err, "result", "failure")
    return err
}
```

**New method `Start(ctx)`:**
```go
// Start launches background goroutines that require an explicit lifecycle
// (currently just the sync worker). Called from main.go after NewController.
func (c *Controller) Start(ctx context.Context) {
    c.syncWorker.Start(ctx)
}
```

### 3.3 `main.go` changes

Add a new goroutine alongside the compensation loop. The wrapper goroutine exists so `wg.Wait()` in shutdown blocks until the worker has fully stopped, not just until `Start` returns:

```go
wg.Add(1)
go func() {
    defer wg.Done()
    appLogger.Info("Starting sync worker")
    controller.Start(ctx)         // non-blocking: spawns worker goroutine, returns immediately
    <-ctx.Done()                  // wait for shutdown signal
    controller.StopSyncWorker()   // blocks until worker goroutine exits
}()
```

Where `StopSyncWorker()` is a thin wrapper:
```go
func (c *Controller) StopSyncWorker() { c.syncWorker.Stop() }
```

### 3.4 Removed code

- `pendingUpdates` field and all reads/writes — was dead state.
- `c.debounceTimer` field — worker owns the timer now.
- The `time.AfterFunc` closure in old `syncToCloudflare` — replaced by worker's `runOnce`.

## 4. Error Handling & Shutdown

### 4.1 Sync failure

`syncFn` returns error → `runOnce`:
1. Stores error in `lastErr` (visible to next `FlushSync`).
2. Logs WARN with the error.
3. Drains `flushQueue` so callers don't hang.
4. Returns to `run` loop.

Worker does **not** auto-retry. Reconciliation ticker (default 120s) and compensation queue drive convergence.

### 4.2 Panic safety

`runOnce` has `defer recover()`. On panic:
1. Logs ERROR with the panic value.
2. Stores synthetic error in `lastErr`.
3. Drains `flushQueue`.
4. Worker **does not exit** — next trigger restarts `runOnce` cleanly.

Rationale: a single bad sync (e.g., CF SDK returns a value that triggers a nil deref in our code) shouldn't permanently wedge the pipeline.

### 4.3 FlushSync semantics

- Returns `nil` if the sync cycle completed without error.
- Returns the stored error if `syncFn` failed.
- Returns `ctx.Err()` if the context cancelled before completion.
- Multiple concurrent `FlushSync` callers all register on `flushQueue`; each gets its own `done` channel. Worker drains all of them after each `runOnce`.

### 4.4 Shutdown sequence

```
main.go:
  sigChan ← SIGTERM
  cancel()                                  // ctx propagated
  controller.ForceSaveState()
  switch cleanup strategy:
    "graceful-cleanup":
      cleanupCtx (with configured timeout)
      controller.CleanupResources(cleanupCtx)
        → FlushSync(cleanupCtx)             // waits for final sync
  wg.Wait()
    → sync worker goroutine sees ctx.Done, returns
    → stopCh closes
```

If `CleanupResources` is skipped (strategy `"fast-exit"`), worker exits via `ctx.Done` without flushing. State on disk + Cloudflare may diverge; reconciliation on next startup recovers.

### 4.5 Edge cases

| Case | Behavior |
|---|---|
| `TriggerSync` before `Start` | Non-blocking send on buffered channel; signal waits until `Start`. Safe. |
| `FlushSync` before `Start` | Returns `errWorkerNotStarted`. |
| `FlushSync` with cancelled ctx | Returns `ctx.Err()` immediately. |
| Worker idle, `Stop` called | Returns immediately (goroutine exits on next ctx.Done). |
| 1000 `TriggerSync` in 1ms | 999 dropped, 1 queued, 1 sync runs. Correct by coalescing invariant. |

## 5. Testing

### 5.1 Worker unit tests (`sync_worker_test.go`)

No Controller, no CF, no Docker. Inject `syncFn` that increments an atomic counter and optionally sleeps / returns error / panics.

| Test | Verifies |
|---|---|
| `TestSyncWorker_TriggersCoalesce` | 10 rapid `TriggerSync` + `FlushSync` → `syncFn` called exactly once. |
| `TestSyncWorker_DebounceHonored` | `TriggerSync`; immediately check counter = 0; wait `debounce + slack`; counter = 1. |
| `TestSyncWorker_FlushSyncBlocks` | `syncFn` blocks on a signal chan; `FlushSync` blocks until signal sent. |
| `TestSyncWorker_FlushSyncReturnsSyncErr` | `syncFn` returns sentinel error → `FlushSync` returns same error. |
| `TestSyncWorker_FlushSyncCtxTimeout` | `FlushSync(cancelledCtx)` → returns `context.Canceled`. |
| `TestSyncWorker_PanicRecovery` | First `syncFn` panics; second `TriggerSync` + `FlushSync` → `syncFn` called again, no deadlock. |
| `TestSyncWorker_ReTriggerAfterSync` | `TriggerSync`; wait for sync to start; `TriggerSync` again; `FlushSync` → counter = 2. |
| `TestSyncWorker_ShutdownClean` | Cancel ctx; `Stop()` returns within 100ms. |
| `TestSyncWorker_FlushBeforeStart` | `FlushSync(ctx)` before `Start` → returns `errWorkerNotStarted`. |

### 5.2 Controller integration tests (`controller_test.go`, new cases)

Use the existing mock `CloudflareManager` (already in test file).

| Test | Verifies |
|---|---|
| `TestController_SyncToCloudflareUsesWorker` | 5 calls to `syncToCloudflare` + `FlushSync` → mock `UpdateConfiguration` called exactly once. |
| `TestController_CleanupResourcesFlushes` | Mock `UpdateConfiguration` blocks; `CleanupResources` does not return until mock returns. |

### 5.3 Existing tests preserved

All current `*_test.go` files continue to pass without edits. `syncToCloudflare` keeps its signature; only its body changes.

### 5.4 Manual verification (post-implementation)

- `make test` green.
- `make fmt-check` passes.
- Run docktunnel locally; start/stop 5 labeled containers in quick succession; `curl /metrics` shows `docktunnel_sync_duration_seconds_count` increments by ~1 (not 5).
- `kill -TERM <pid>`; logs show `"Resources cleaned up successfully"` after the final `performSync` log line, not before.

## 6. Rollout

Single PR. No flags, no migration. The change is mechanical:

1. Add `sync_worker.go` + `sync_worker_test.go`.
2. Edit `controller.go`: remove `debounceTimer`/`pendingUpdates`, add `syncWorker` field, rewrite `syncToCloudflare`, edit `CleanupResources`, add `Start`/`StopSyncWorker`.
3. Edit `main.go`: add goroutine calling `controller.Start(ctx)`.
4. Run `go test ./...` and `gofmt -s -w .`.
5. Manual smoke test per §5.4.

## 7. Open Questions

None at spec approval time. Implementation may surface:
- Whether `StopSyncWorker` should be exposed or inlined in `main.go`. (Current design: exposed for symmetry with `Start`.)
- Whether `started atomic.Bool` should be `sync.Once` instead. (`CompareAndSwap` is more lenient about being called from multiple goroutines.)
