# Controller Phase 2: Split God Object

**Date:** 2026-06-19
**Status:** Approved
**Depends on:** Phase 1 (`syncWorker`, merged on `feature/sync-worker`)

## Problem

After Phase 1 (`syncWorker` serialization), `controller.go` is still a 1244-line god object. Lifecycle, dispatch, container handlers, sync, state persistence, compensation, GC, reconcile, and diagnostics all share one file. Any further work — per-hostname sync state, persistence hooks, future feature development — becomes much harder to land cleanly inside that file.

A secondary issue surfaced during exploration: `state.Manager` has a complete flapping-detection API (`CheckFlapping` / `RecordTransition` / `MarkAsFlapping` / `GetFlappingState`) that is **never called by production code** — only by its own unit tests. The controller uses its own `containerHealth` / `isFlapping` / `updateContainerHealth`. The state-side code is dead.

## Scope

In scope (single phase, executed on `feature/sync-worker`):

- Split `controller.go` into focused files. Extract 7 helper structs (`dispatcher`, `syncer`, `reconciler`, `healthTracker`, `gc`, `compensation`, `diagnostics`). Controller becomes an orchestrator.
- Delete `state.Manager`'s dead flapping code + corresponding tests.

Out of scope (explicitly deferred):

- Per-hostname sync state tracking.
- Persistence hook changes.
- Typed `DesiredState` struct (deferred since Phase 1).
- True dependency injection (helpers taking data as parameters). This phase uses bidirectional references; helpers hold a `*Controller` pointer. Decoupling the data flow is a separate future phase.
- `DesiredState` / per-resource state machines / journal persistence — all deferred.

## Non-Goals

- **No public API changes** to `Controller`. All exported method signatures remain identical.
- **No behavior changes.** Pure refactor + dead-code removal. All existing tests pass without edits.
- **No new background goroutines.** syncer still rides on the existing `syncWorker` goroutine.
- **No new metrics.** Existing counters/histograms behave identically.

---

## 1. Architecture (target, after Phase 2 complete)

```
Controller (orchestrator)
├── dockerManager
├── cloudflareManager
├── stateManager          // state.Manager with dead flapping code removed
├── ruleValidator
├── ingressRules          // desired state: hostname → rule
├── containerRules        // reverse index: containerID → hostnames
├── mu                    // protects the two maps above
├── syncer                // owns syncWorker reference + actualState cache
├── reconciler            // stateless; operates on Controller desired state
├── healthTracker         // owns containerHealth + own mu + flapping config
├── dispatcher            // stateless event router
├── gc                    // stateless shim
├── compensation          // stateless shim
└── diagnostics           // reads desired + actual (actual owned by syncer)
```

**Data flow (unchanged from Phase 1):**
event → `dispatcher.Dispatch` → handler → mutate desired state under `c.mu` → `syncer.TriggerSync()` → syncWorker goroutine → `performSync` → CF API.

**Connection style — bidirectional references:**
- Helper structs hold a `c *Controller` pointer.
- Methods access `c.ingressRules`, `c.cloudflareManager`, etc. directly.
- This is **not** true decoupling — it's relocation. The point of this phase is to get code into the right files; decoupling the data flow is a separate future phase.

## 2. File Layout (`internal/controller/`)

| File | Contents | Approx lines |
|---|---|---|
| `controller.go` | Controller struct, NewController, Start, StopSyncWorker, CleanupResources | ~180 |
| `dispatcher.go` | dispatcher struct, Dispatch/dispatchInner/isDocktunnelEnabled | ~80 |
| `handlers.go` | handleContainerStart, handleContainerStop, handleHealthHealthy, handleHealthUnhealthy, registerContainerRules, getServiceRetentionPolicy | ~280 |
| `syncer.go` | syncer struct, Sync, performSync, syncDNSRecords, syncToCloudflare, GetIngressRules, refreshActualState, setLastKnownActualRules | ~350 |
| `reconciler.go` | reconciler struct, Reconcile, handleResync, ReconcileEnabled, ReconcileInterval | ~140 |
| `health.go` | healthTracker struct, isFlapping, updateContainerHealth, ContainerHealth type | ~110 |
| `gc.go` | RunGarbageCollection, updateRetentionGauge | ~80 |
| `compensation.go` | ExecuteAction, RunCompensationLoop, SetCompensationConfig, SetCompensationQueueCap, SetPersistenceConfig | ~60 |
| `diagnostics.go` | GetDebugState, snapshotRuleViewsLocked | ~60 |
| `sync_worker.go` | unchanged from Phase 1 | 165 |
| `validator.go` | unchanged | 162 |

## 3. Helper Struct Shape

Example — `syncer`:

```go
type syncer struct {
    c   *Controller
    log *slog.Logger

    // Cached actual state for /debug/state.
    // Moves here from Controller (was: actualStateMu + lastKnownActualRules).
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

### 3.1 Field ownership changes

These fields move **out of `Controller`** into the named helper struct:

| Field | Moves to | Notes |
|---|---|---|
| `actualStateMu sync.RWMutex` | `syncer` | Used only by actual-state cache |
| `lastKnownActualRules []diagnostics.RuleView` | `syncer` | Same |
| `containerHealth map[string]*ContainerHealth` | `healthTracker` | Owns its own mutex now |
| `flappingWindow time.Duration` | `healthTracker` | Config moves with the data |
| `flappingThreshold int` | `healthTracker` | |
| `coolingPeriod time.Duration` | `healthTracker` | |
| `maxCoolingPeriod time.Duration` | `healthTracker` | |

These fields **stay on `Controller`** (shared by multiple helpers):

- `dockerManager`, `cloudflareManager`, `stateManager`, `ruleValidator`
- `ingressRules`, `containerRules`, `mu`
- `syncWorker` (syncer accesses it via `s.c.syncWorker`)
- `debounceDuration`, `reconcileEnabled`, `reconcileInterval`

### 3.2 Dispatcher — preserving public API

`dispatcher.Dispatch` is called from `main.go`'s event loop via `controller.Dispatch`. To preserve the public API, `Controller` keeps a `Dispatch(ctx, event)` method that delegates:

```go
// controller.go
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
    return c.dispatcher.Dispatch(ctx, event)
}
```

The metrics recording (`metrics.RecordEvent`) currently lives in `Controller.Dispatch` — it moves with the body into `dispatcher.Dispatch`, but the public delegation still happens on `Controller.Dispatch`.

Same delegation pattern for any other method called from outside the package (`main.go`, `internal/server` diagnostics, etc.). Audit call sites in §5.

### 3.3 Health tracker — own mutex

`healthTracker` owns its own `sync.Mutex` (not `sync.RWMutex` — all access is read-modify-write). This eliminates lock-ordering concerns with `c.mu`:

```go
type healthTracker struct {
    c   *Controller
    log *slog.Logger

    mu             sync.Mutex
    containerHealth map[string]*ContainerHealth

    flappingWindow    time.Duration
    flappingThreshold int
    coolingPeriod     time.Duration
    maxCoolingPeriod  time.Duration
}

func (h *healthTracker) isFlapping(containerID string) bool { ... }
func (h *healthTracker) updateContainerHealth(containerID string, isStartEvent bool) { ... }
```

Callers (handlers) currently call `c.isFlapping(...)` and `c.updateContainerHealth(...)` under `c.mu`. After extraction, they call `c.health.isFlapping(...)` etc. **without** holding `c.mu` — `healthTracker` manages its own locking.

This is the one place where lock semantics change. Audit each call site during extraction.

## 4. Dead Flapping Code Removal

In `internal/state/manager.go`, delete:

- Field: `flappingStates` map (and its initialization in `NewManager`)
- Methods: `CheckFlapping`, `RecordTransition`, `MarkAsFlapping`, `GetFlappingState`
- Any internal helper types those methods depend on (e.g., `flappingState` struct, related constants)

In `internal/state/manager_test.go`, delete the corresponding tests:
- `TestManager_CheckFlapping*`
- `TestManager_RecordTransition*`
- `TestManager_MarkAsFlapping*`
- `TestManager_GetFlappingState*`

In `pkg/types/tunnel.go`, check whether `FlappingState` type is still referenced after the above deletions. If not, delete it. Verify with `grep -rn 'FlappingState' /workspace --include='*.go'` before deletion; expected result: zero hits outside the deletion targets.

**Migration of live flapping logic** (the controller's `containerHealth` path): moves to `healthTracker`. The `ContainerHealth` struct definition (currently in `controller.go`) moves to `health.go`. Behavior unchanged.

## 5. External Call Site Audit

Before extracting each helper, identify external callers (outside `package controller`) of the methods being moved. Each must keep working via delegation on `Controller`.

```bash
# Find every Controller method called from outside package controller.
grep -rn 'controller\.\(Dispatch\|Sync\|Reconcile\|ReconcileEnabled\|ReconcileInterval\|CleanupResources\|RunGarbageCollection\|RunCompensationLoop\|ExecuteAction\|SetCompensationConfig\|SetCompensationQueueCap\|SetPersistenceConfig\|SetStatePath\|LoadState\|SaveState\|SaveStateIfDirty\|ForceSaveState\|GetDebugState\|Start\|StopSyncWorker\|GetIngressRules\)' /workspace --include='*.go' | grep -v '_test.go' | grep -v 'internal/controller/'
```

Known external callers (verify during implementation):

| Method | Caller |
|---|---|
| `Dispatch` | `cmd/docktunnel/main.go` event loop |
| `Sync` | `cmd/docktunnel/main.go` initial sync |
| `Reconcile`, `ReconcileEnabled`, `ReconcileInterval` | `cmd/docktunnel/main.go` reconcile ticker |
| `RunGarbageCollection` | `cmd/docktunnel/main.go` GC ticker |
| `RunCompensationLoop` | `cmd/docktunnel/main.go` compensation goroutine |
| `SetCompensationConfig`, `SetCompensationQueueCap`, `SetPersistenceConfig` | `cmd/docktunnel/main.go` startup |
| `SetStatePath`, `LoadState`, `ForceSaveState` | `cmd/docktunnel/main.go` startup/shutdown |
| `GetDebugState` | `internal/server` debug endpoint |
| `GetIngressRules` | `cmd/docktunnel/main.go` cleanup logging + debug |
| `Start`, `StopSyncWorker` | `cmd/docktunnel/main.go` lifecycle |
| `CleanupResources` | `cmd/docktunnel/main.go` shutdown |

All of these stay as `Controller` methods (delegating to the appropriate helper).

## 6. Testing

- `controller_test.go` (997 lines): **untouched**. This is the regression boundary. If any test fails, the split was wrong.
- `sync_worker_test.go`, `validator_test.go`: untouched.
- `state/manager_test.go`: 4 dead flapping tests deleted (see §4).
- New `health_test.go`: minimal — verify `newHealthTracker` returns non-nil; one positive and one negative case for `isFlapping`; one case for `updateContainerHealth` exponential backoff. (The detailed coverage stays in `controller_test.go`.)
- New `syncer_test.go`: minimal — verify `newSyncer` returns non-nil; one case for `setLastKnownActualRules` + `GetDebugState` reading from the cache.

The new test files prove the helpers can be constructed and exercised in isolation; they are not comprehensive (the existing controller tests cover the integration).

## 7. Error Handling

No error paths change. Methods return the same errors from the same places.

## 8. Shutdown Interaction

Already correct after Phase 1's C1+C2 fix. This phase's syncer extraction must preserve the property: `CleanupResources` → `c.syncer.FlushSync(ctx)` → worker still alive → sync lands → return.

Concretely: `Controller.CleanupResources` (in `controller.go`, not moved) becomes:

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

Where `syncer.FlushSync` is a thin delegation: `func (s *syncer) FlushSync(ctx context.Context) error { return s.c.syncWorker.FlushSync(ctx) }`.

## 9. Rollout Plan

Single phase, 7 commits, all on `feature/sync-worker`. Each commit must leave `go test ./...` + `go vet ./...` + `gofmt -s -l .` clean.

Recommended order (stateless shims first, stateful extractions last):

1. `refactor(controller): extract dispatcher` — easiest, no state
2. `refactor(controller): extract reconciler` — stateless, reads Controller maps under c.mu
3. `refactor(controller): extract gc/compensation/diagnostics shims` — single commit, all stateless
4. `refactor(controller): extract syncer` — largest, moves actualState cache; `CleanupResources` rewritten to delegate
5. `refactor(controller): extract healthTracker` — moves containerHealth + 4 flapping config fields; lock semantics shift (handlers stop holding c.mu around health calls)
6. `refactor(state): delete dead flapping code` — `state.Manager` methods + tests + `FlappingState` type if unused
7. `test(controller): add healthTracker + syncer unit tests`

After all 7: dispatch one final whole-implementation review subagent against `main..HEAD` to catch regressions. Then `superpowers:finishing-a-development-branch`.

## 10. Open Questions

None at spec approval. Implementation may surface:

- Whether `healthTracker` should own its mutex or share `c.mu`. Current design: own mutex. Reduces `c.mu` contention and removes a would-be lock ordering constraint between handlers and health checks.
- Whether `dispatcher` deserves to be a struct or could be free functions. Struct chosen for symmetry with other helpers; the struct holds no state.
- Whether the new `health_test.go` and `syncer_test.go` should live in `internal/controller/` alongside the source. Yes — same package pattern as existing test files.
