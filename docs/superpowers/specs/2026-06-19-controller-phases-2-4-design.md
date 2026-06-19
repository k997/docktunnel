# Controller Phases 2–4: Split, Per-Hostname State, Persistence Hooks

**Date:** 2026-06-19
**Status:** Approved
**Depends on:** Phase 1 (`syncWorker`, merged on `feature/sync-worker`)

## Problem

After Phase 1 (`syncWorker` serialization), three architectural issues remain on the controller side:

1. **`controller.go` is a 1244-line god object.** Lifecycle, dispatch, container handlers, sync, state persistence, compensation, GC, reconcile, and diagnostics all share one file. Phase 3 (per-hostname state) and Phase 4 (persistence hooks) become much harder to land cleanly inside that file.

2. **Sync is all-or-nothing.** `performSync` pushes the entire `ingressRules` map plus a batched DNS sync. A single hostname's DNS failure marks the whole sync failed; the next debounce cycle retries everything. With N hostnames and one flaky DNS, this wastes N-1 worth of CF API calls per retry.

3. **`SaveIfDirty` is only called from a 60s ticker in `main.go`.** Container stops within that window are lost on crash. Reconcile restores desired state from Docker, but losing 60s of retention transitions can leave `state.Manager` out of sync with what was actually pushed to Cloudflare.

A secondary issue surfaced during exploration: `state.Manager` has a complete flapping-detection API (`CheckFlapping`/`RecordTransition`/`MarkAsFlapping`/`GetFlappingState`) that is **never called by production code** — only by its own unit tests. The controller uses its own `containerHealth`/`isFlapping`/`updateContainerHealth`. The state-side code is dead.

## Scope

In scope (three phases, executed in order on `feature/sync-worker`):

- **Phase 2:** Split `controller.go` into focused files. Extract 7 helper structs (`dispatcher`, `syncer`, `reconciler`, `healthTracker`, `gc`, `compensation`, `diagnostics`). Controller becomes an orchestrator. Delete `state.Manager`'s dead flapping code.
- **Phase 3:** Per-hostname sync state tracking inside `syncer`. Selective DNS retry. Early-exit on no-drift.
- **Phase 4:** Hook `SaveIfDirty` into every state-changing entrypoint on `Controller`.

Out of scope:

- Typed `DesiredState` struct (was deferred in Phase 1; remains deferred).
- DNS / Route independent state machines (state space doubles for little gain; CF API client fails atomically).
- K8s-controller-pattern full lifecycle (`Missing → Creating → Active → Deleting`). `state.Manager` already has `Transition()`; don't duplicate.
- WAL / journal file format. Atomic-write snapshots + backup rotation are sufficient.
- Persistence format change. gob stays.

## Non-Goals

- **No public API changes** to `Controller`. All exported method signatures remain identical.
- **No behavior changes in Phase 2.** Pure refactor + dead-code removal. All existing tests pass without edits.
- **No new background goroutines.** syncer still rides on the existing `syncWorker` goroutine.
- **No metrics surface changes** in Phase 3 (though `docktunnel_sync_duration_seconds` naturally stops counting no-op syncs once early-exit lands).

---

## 1. Architecture (target, after Phase 3 complete)

```
Controller (orchestrator)
├── dockerManager
├── cloudflareManager
├── stateManager          // state.Manager with dead flapping code removed
├── ruleValidator
├── ingressRules          // desired state: hostname → rule
├── containerRules        // reverse index: containerID → hostnames
├── mu                    // protects the two maps above
├── syncer                // owns syncWorker + actualState cache + hostnameStatus
├── reconciler            // stateless; operates on Controller desired state
├── healthTracker         // owns containerHealth + own mu + flapping config
├── dispatcher            // stateless event router
├── gc                    // stateless shim
├── compensation          // stateless shim
└── diagnostics           // reads desired + actual (actual owned by syncer)
```

**Data flow (unchanged from Phase 1):**
event → `dispatcher.Dispatch` → handler → mutate desired state under `c.mu` → `syncer.TriggerSync()` → syncWorker goroutine → `performSync` → CF API.

**Connection style — Phase 2 uses bidirectional references:**
- Helper structs hold a `c *Controller` pointer.
- Methods access `c.ingressRules`, `c.cloudflareManager`, etc. directly.
- This is **not** true decoupling — it's relocation. The point of Phase 2 is to get code into the right files so Phase 3 can do the real decoupling inside `syncer`.
- True dependency injection (helpers take data as parameters) is deferred to a future phase.

## 2. Phase 2 Detail

### 2.1 File layout (`internal/controller/`)

| File | Contents | Approx lines |
|---|---|---|
| `controller.go` | Controller struct, NewController, Start, StopSyncWorker, CleanupResources | ~180 |
| `dispatcher.go` | dispatcher struct, Dispatch/dispatchInner/isDocktunnelEnabled | ~80 |
| `handlers.go` | handleContainerStart, handleContainerStop, handleHealthHealthy, handleHealthUnhealthy, registerContainerRules, getServiceRetentionPolicy | ~280 |
| `syncer.go` | syncer struct, Sync, performSync, syncDNSRecords, syncToCloudflare, GetIngressRules, refreshActualState, setLastKnownActualRules | ~350 |
| `reconciler.go` | reconciler struct, Reconcile, handleResync, ReconcileEnabled, ReconcileInterval | ~140 |
| `health.go` | healthTracker struct, isFlapping, updateContainerHealth | ~110 |
| `gc.go` | RunGarbageCollection, updateRetentionGauge | ~80 |
| `compensation.go` | ExecuteAction, RunCompensationLoop, SetCompensationConfig, SetCompensationQueueCap, SetPersistenceConfig | ~60 |
| `diagnostics.go` | GetDebugState, snapshotRuleViewsLocked | ~60 |
| `sync_worker.go` | unchanged from Phase 1 | 165 |
| `validator.go` | unchanged | 162 |

### 2.2 Helper struct shape (example: `syncer`)

```go
type syncer struct {
    c   *Controller
    log *slog.Logger

    // Cached actual state for /debug/state.
    actualStateMu        sync.RWMutex
    lastKnownActualRules []diagnostics.RuleView
}

func newSyncer(c *Controller, log *slog.Logger) *syncer {
    return &syncer{c: c, log: log}
}

func (s *syncer) Sync(ctx context.Context) error { ... }
func (s *syncer) performSync(ctx context.Context) error { ... }
// etc.
```

Controller holds `syncer *syncer` (lowercase = unexported). `NewController` calls `newSyncer(controller, slog.Default())` and stores the result.

**Migration rule:** method bodies are copied verbatim. The receiver `c *Controller` becomes `s *syncer`, and references to other Controller fields go through `s.c.X`. No logic changes.

> Phase 3 rewrites `performSync`'s body. Phase 2 only relocates the existing implementation; the rewrite is a separate, later set of commits so the diff stays reviewable.

### 2.3 Dispatcher — special case

`dispatcher.Dispatch` is called externally (from `main.go`'s event loop). To preserve the public API, `Controller` keeps a `Dispatch(ctx, event)` method that delegates:

```go
// controller.go
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
    return c.dispatcher.Dispatch(ctx, event)
}
```

The metrics recording (`metrics.RecordEvent`) currently lives in `Dispatch` — it moves with the body into `dispatcher.Dispatch`, but the public delegation still happens on `Controller.Dispatch`.

### 2.4 Dead flapping code removal

In `internal/state/manager.go`, delete:
- Field: `flappingStates map[string]*flappingState` (and its initialization in `NewManager`)
- Methods: `CheckFlapping`, `RecordTransition`, `MarkAsFlapping`, `GetFlappingState`
- Any helper types those methods depend on (e.g., internal `flappingState` struct)

In `internal/state/manager_test.go`, delete the corresponding tests.

In `pkg/types/tunnel.go`, evaluate `FlappingState` type — if nothing else uses it, delete; otherwise leave. Verify via `grep -rn 'FlappingState'` before deletion.

**Migration of live flapping logic** (the controller's `containerHealth` path): moves to `healthTracker`. The `ContainerHealth` struct definition (currently in `controller.go`) moves to `health.go`. Behavior unchanged.

### 2.5 Phase 2 testing

- `controller_test.go` (997 lines): **untouched**. If any test fails, the split was wrong.
- New `health_test.go`: migrate flapping-related assertions from `controller_test.go` (without modifying the original tests — duplication is OK during the transition; the new file proves `healthTracker` works in isolation).
- New `syncer_test.go`: minimal — verify `newSyncer` returns non-nil, actualState cache read/write.
- `state/manager_test.go`: delete the 4 dead flapping tests.

### 2.6 Phase 2 rollout

Single logical commit per file extracted (8–10 commits). Recommended order (each must leave tests green):

1. `refactor(controller): extract dispatcher` (simplest, no state)
2. `refactor(controller): extract reconciler` (stateless)
3. `refactor(controller): extract gc, compensation, diagnostics shims` (can be one commit, all stateless)
4. `refactor(controller): extract syncer` (largest, has state — last among the big ones)
5. `refactor(controller): extract healthTracker` (moves containerHealth)
6. `refactor(state): delete dead flapping code`
7. `test(controller): add healthTracker unit tests`

After Phase 2: full `go test ./...` + `go vet ./...` + `gofmt -s -l .` clean. Manual spot-check: docktunnel starts, container start/stop triggers sync, metrics endpoint serves.

## 3. Phase 3 Detail

### 3.1 New state in `syncer`

```go
type hostnameSyncState int

const (
    stateSynced    hostnameSyncState = iota // last push succeeded
    statePendingRoute                       // ingress push needs retry
    statePendingDNS                         // DNS upsert needs retry (route OK)
    stateFailed                             // DNS delete failed; waiting for reconcile
)

type hostnameStatus struct {
    state     hostnameSyncState
    lastErr   error
    lastTry   time.Time
    failCount int
}

type syncer struct {
    c   *Controller
    log *slog.Logger

    actualStateMu        sync.RWMutex
    lastKnownActualRules []diagnostics.RuleView

    // statusMu is only touched from the syncWorker goroutine (single-threaded
    // by Phase 1's invariant), so no lock needed in practice. RWMutex kept
    // for defensive reads from /debug/state.
    statusMu sync.RWMutex
    statuses map[string]*hostnameStatus
}
```

### 3.2 `performSync` rewrite

```
1. snapshot desired ingressRules under c.mu.RLock
2. diff desired vs. lastPushed (a new syncer field)
   → added, updated, removed, unchanged sets
3. if all four sets are empty AND every existing status == stateSynced:
   → early-exit, no CF API calls, metrics.ObserveSyncDuration(0)
4. call UpdateConfiguration with full desired list (CF API is whole-document)
   on success: mark every hostname in desired as stateSynced; drop statuses for removed hostnames
   on failure: mark every hostname in desired as statePendingRoute with err
5. for each hostname in added ∪ updated:
       call UpsertDNSRecords([hostname])   // per-hostname, not batched
       on success: stateSynced
       on failure: statePendingDNS, store err
6. for each hostname in removed:
       call DeleteDNSRecords([hostname])
       on success: delete status entry
       on failure: stateFailed, store err
7. update lastPushed = snapshot of desired
```

**Why DNS goes per-hostname but route stays whole-document:** the Cloudflare Tunnel config API replaces the entire ingress list — there is no per-hostname endpoint. The DNS API, in contrast, exposes per-record create/delete. Selective retry is therefore only possible for DNS.

### 3.3 Coalescing across cycles

`TriggerSync` still signals the worker. The worker's debounce still collapses 100 triggers into one sync. The new behavior is:

- A sync cycle that produced only `statePendingDNS` hostnames (no `statePendingRoute`) skips step 4 (route push) entirely on the next cycle, since the desired route list hasn't changed.
- A sync cycle that produced `statePendingRoute` retries the whole-document push.

This is the source of the API call savings.

### 3.4 Metrics

- Existing: `docktunnel_sync_duration_seconds` — unchanged, observes every cycle.
- Existing: `docktunnel_dns_sync_operations_total{op="upsert|delete",result="success|failure"}` — now incremented per-hostname instead of per-batch. The metric name doesn't change but the count semantics shift; document this in the metric help string.
- New (optional, defer if not requested): `docktunnel_hostname_sync_state{hostname,state}` gauge — high cardinality on hostname, only enable if explicitly requested.

### 3.5 Phase 3 testing

Extend `syncer_test.go`:

- `TestSyncer_AllSynced`: all hostnames succeed → statuses empty or all `stateSynced`.
- `TestSyncer_RouteFailure`: `UpdateConfiguration` returns error → all hostnames `statePendingRoute`; next cycle retries whole-document push.
- `TestSyncer_DNSFailureIsolated`: hostname A DNS upsert fails, B succeeds → A = `statePendingDNS`, B = `stateSynced`; next cycle only A's DNS is retried.
- `TestSyncer_DNSDeleteFailure`: removed hostname's DNS delete fails → hostname marked `stateFailed`.
- `TestSyncer_EarlyExitNoDrift`: desired unchanged + all synced → no CF API calls (assert mock counters zero).
- `TestSyncer_DNSRetryOnlyFailures`: after a partial DNS failure, the next sync only calls `UpsertDNSRecords` for failed hostnames, not all.

Mock extension: `mockCloudflareManager` gains a `dnsUpsertError map[string]error` and `dnsDeleteError map[string]error` for per-hostname failure injection. Existing call sites unaffected (zero-value maps = no failures).

### 3.6 Phase 3 rollout

5 commits:

1. `feat(syncer): add hostnameSyncState + status tracking`
2. `refactor(syncer): split DNS upsert per-hostname`
3. `feat(syncer): selective DNS retry`
4. `feat(syncer): early-exit when no drift`
5. `test(syncer): per-hostname failure injection tests`

## 4. Phase 4 Detail

### 4.1 What changes

`state.Manager` is unchanged. `markDirty()` already fires on every Add/Remove/Transition. `SaveIfDirty()` already throttles.

The change is at the `Controller` layer: call `c.SaveStateIfDirty()` (which delegates to `stateManager.SaveIfDirty()`) at the end of every state-changing entrypoint:

| Method | When |
|---|---|
| `handleContainerStart` | after `syncToCloudflare` returns |
| `handleContainerStop` | after the transition + syncToCloudflare sequence |
| `handleHealthHealthy` | after `syncToCloudflare` |
| `handleHealthUnhealthy` | after `syncToCloudflare` |
| `RunGarbageCollection` | after the post-GC sync, before returning |
| `Reconcile` | only if drift was detected and synced (skip on no-drift early-exit) |
| `ExecuteAction` | after the compensation action succeeds |
| `CleanupResources` | not needed (process is shutting down; `ForceSaveState` in main.go covers it) |

`SaveIfDirty` is internally throttled, so calling it 10 times in 1s results in 0 or 1 disk writes. The throttle is the existing `state.Manager` behavior — no new config knob.

### 4.2 What does NOT change

- gob format
- Atomic write semantics (`tmp + rename`)
- Backup rotation (`.bak.1`, `.bak.2`, ...)
- JSON fallback loader
- No WAL, no journal file
- No new config options

### 4.3 Phase 4 testing

- `state/persistence_test.go`: unchanged.
- New in `controller_test.go` (or new `controller_persistence_test.go`): inject a `mockStateManager` that counts `SaveIfDirty` calls. Verify each entrypoint in §4.1 triggers a call.
- Existing integration tests (`TestStartupReconciliation`, `TestExecuteAction_*`, etc.) pass unchanged.

### 4.4 Phase 4 rollout

2 commits:

1. `feat(controller): SaveIfDirty after state-changing operations`
2. `test(controller): verify SaveIfDirty call sites`

## 5. Error Handling

- **Phase 2:** no error paths change. Methods return the same errors from the same places.
- **Phase 3:** syncer logs the per-hostname state at INFO level when a hostname enters `statePendingDNS` or `stateFailed` (single line per hostname per state change, not per retry). `statePendingRoute` is logged at DEBUG to avoid spam during CF outages.
- **Phase 3 panic safety:** if DNS-upsert loop panics (e.g., nil deref on a malformed CF response), the existing `syncWorker` panic recovery (Phase 1) catches it; statuses for unprocessed hostnames stay at their previous value, picked up next cycle.
- **Phase 4:** `SaveIfDirty` errors are logged WARN (existing behavior in `main.go`'s GC ticker). New call sites do the same.

## 6. Shutdown interaction

Already correct after Phase 1's C1+C2 fix. Phase 2's syncer extraction must preserve the property: `CleanupResources` → `c.syncer.FlushSync(ctx)` → worker still alive → sync lands → return.

Concretely: `Controller.CleanupResources` (in `controller.go`, not moved) now reads:

```go
func (c *Controller) CleanupResources(ctx context.Context) error {
    c.mu.Lock()
    c.ingressRules = make(...)
    c.containerRules = make(...)
    c.mu.Unlock()

    if err := c.syncer.FlushSync(ctx); err != nil { ... }
    return nil
}
```

Where `syncer.FlushSync` is a thin delegation: `func (s *syncer) FlushSync(ctx context.Context) error { return s.c.syncWorker.FlushSync(ctx) }`. This keeps the call site readable while preserving the worker lifecycle.

## 7. Testing strategy summary

| Phase | Existing tests | New tests | Deleted tests |
|---|---|---|---|
| 2 | all pass unchanged | `health_test.go`, `syncer_test.go` minimal | 4 dead flapping tests in `state/manager_test.go` |
| 3 | all pass unchanged | 6 new `syncer_test.go` cases for state tracking + retry | none |
| 4 | all pass unchanged | 1 new test verifying SaveIfDirty call sites | none |

`controller_test.go` stays untouched across all three phases. It's the regression boundary.

## 8. Rollout plan

Three phases, each landing on `feature/sync-worker` as a series of focused commits.

```
Phase 2 (7 commits, ~1 day):
  - extract dispatcher
  - extract reconciler
  - extract gc, compensation, diagnostics shims (one commit)
  - extract syncer (largest)
  - extract healthTracker
  - delete dead state.Manager flapping code
  - new health_test.go + syncer_test.go minimal

Phase 3 (5 commits, ~1 day):
  - add hostnameSyncState + status tracking
  - split DNS upsert per-hostname
  - selective DNS retry
  - early-exit on no drift
  - per-hostname failure injection tests

Phase 4 (2 commits, ~2 hours):
  - SaveIfDirty hooks
  - verify-SaveIfDirty test
```

Between phases: full `go test ./...` + `go vet ./...` + `gofmt -s -l .` must be clean.

After all three phases: dispatch one final whole-implementation review subagent against `main..HEAD` to catch cross-phase regressions. Then `superpowers:finishing-a-development-branch`.

## 9. Open Questions

None at spec approval. Implementation may surface:

- Whether `healthTracker` should own its config (flappingWindow/Threshold/etc.) or read from Controller. Current design: owned (moved out of Controller fields). Reduces Controller field count by 4.
- Whether `dispatcher` deserves to be a struct or could be free functions. Struct chosen for symmetry with other helpers; struct has no state.
- Whether `mockStateManager` should be added to `tests/mocks/` or stay local to the new persistence test. Local is simpler; promote later if reused.
