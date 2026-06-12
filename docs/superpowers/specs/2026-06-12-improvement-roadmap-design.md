# DockTunnel Improvement Roadmap — Design Spec

Date: 2026-06-12
Status: Draft

## Scope & Principles

- This spec covers 6 phases of improvement derived from code review.
- Priority order: `Correctness > Consistency > Observability > Performance`.
- Change strategy: tests and feature flags first, incremental rollout, no big-bang rewrites.
- Delivery: Phase 1-2 deep design, Phase 3-6 sketches.
- Implementation plan produced for Phase 1 only; subsequent phases get their own plans.

## Phase 1 (P0, 1-2 weeks): Reliability Main Chain

### Goal

Guarantee that container lifecycle events always converge to the correct tunnel state.

### 1.1 Periodic Reconciliation

**Problem**: Only event-driven updates + startup `Sync()`. Missed events cause permanent drift.

**Design**: Add a reconcile ticker alongside the existing GC ticker in `main.go`.

- Default interval: 120s, configurable via `controller.reconcileInterval`.
- Calls a new `Reconcile(ctx)` method (distinct from `Sync()`).

**`Reconcile()` logic**:

1. Scan running Docker containers → compute desired active rules.
2. Merge retaining entries from `stateManager` (retention routes stay active).
3. Diff against current `ingressRules`.
4. If diff exists, call `syncToCloudflare()` (goes through debounce).
5. If no diff, skip — zero API overhead.

**Feature flag**: `controller.reconcileEnabled` (default `true`). When `false`, skip reconcile ticker entirely, event-only path. One-switch rollback.

**Concurrency**: Reconcile and event path share `syncToCloudflare()` → debounce → `performSync()`. The debounce timer naturally coalesces concurrent triggers into one API call.

### 1.2 Idempotent Operations

**Current state**:

| Operation | Idempotent? | Issue |
|-----------|-------------|-------|
| `UpdateConfiguration` | Yes | Full replacement |
| `UpsertDNSRecords` | Yes | Cloudflare upsert semantics |
| `DeleteDNSRecords` | Yes | No-op if target absent |
| `registerContainerRules` | Mostly | Re-overwrites with same data |
| `handleContainerStop` | Partially | Retention policy falls back to `Immediate` when `ContainerInfo` is nil |

**Fix — Retention policy persistence**:

On `registerContainerRules` success, store the parsed retention policy in `stateManager` as part of the `TunnelEntry`. On `handleContainerStop`, read the policy from `stateManager` instead of `event.ContainerInfo.Labels`.

This makes stop behavior consistent regardless of whether the stop/die event carries container info.

**Fix — Reconcile path uses debounce**:

`Reconcile()` calls `syncToCloudflare()` (debounced), never calls `performSync()` directly. This ensures:
- Only one `performSync` runs at a time.
- Rapid successive changes within the debounce window merge into one API call.
- Reconcile adds no extra API pressure when nothing changed.

### 1.3 Exit Behavior Classification

**Current**: `cleanup.onExit` (bool) + 30s timeout context. No middle ground.

**New configuration**:

```yaml
cleanup:
  strategy: graceful-cleanup   # fast-exit | graceful-cleanup
  timeout: 30s                 # graceful mode timeout
  stateFile: /var/lib/docktunnel/state.bin
```

`cleanup.onExit` is deprecated but backward-compatible: `onExit: true` maps to `strategy: graceful-cleanup`.

**Behaviors**:

| Step | `fast-exit` | `graceful-cleanup` |
|------|-------------|---------------------|
| Save state | `ForceSaveState()` | `ForceSaveState()` |
| Clean Cloudflare | Skip | `CleanupResources(timeoutCtx)` |
| Timeout handling | N/A | Log cleaned/remaining hostnames, then exit |
| Wait goroutines | `wg.Wait()` | `wg.Wait()` |

**`graceful-cleanup` timeout**: On timeout, log a summary:

```
Cleanup completed with partial results:
  cleaned=3 hostnames,
  remaining=["app.example.com", "api.example.com"],
  reason="timeout after 30s"
```

Next startup `Sync()` + `Reconcile()` will naturally fix residuals.

**`fast-exit` use case**: Orchestrated environments (K8s, Docker Compose restart) where external scheduler recreates the instance — cleanup would cause service interruption.

### 1.4 Testing & Acceptance

- **Scenarios**: rapid start/stop cycles, simulated event loss, process restart auto-convergence.
- **Acceptance criteria**:
  - After container stop, state converges within 30-60s window.
  - No permanently stale DNS/Ingress records.
  - No deadlocks or goroutine leaks under high-concurrency events.

### 1.5 Risks & Mitigation

| Risk | Mitigation |
|------|-----------|
| Reconcile frequency causes API pressure | Configurable interval + rate limiter + debounce + no-op on no-diff |
| Reconcile conflicts with event path | Shared debounce coalesces concurrent triggers |
| Feature regression | `reconcileEnabled` flag for one-switch rollback |

---

## Phase 2 (P0/P1, 1 week): Unified State Machine & Retention Semantics

### Goal

Route lifecycle driven by a unified state machine. `Immediate/Timed/Forever` behavior consistent across Ingress, DNS, and State layers.

### 2.1 State Machine

**Four states**:

| State | Ingress | DNS | containerRules | Meaning |
|-------|---------|-----|----------------|---------|
| `Active` | Keep | Keep | Has mapping | Container running, route available |
| `Retaining` | Keep | Keep | Cleared | Container stopped, retention not expired |
| `PendingDelete` | Delete | Delete | Cleared | Immediate or retention expired, awaiting cleanup |
| `Deleted` | — | — | — | Terminal, entry removed |

**State transitions**:

```
                    ContainerStarted
                         │
                         ▼
                    ┌─────────┐
          ┌────────│  Active  │────────┐
          │        └─────────┘        │
          │ ContainerStopped           │ RetentionExpired /
          │ Immediate                  │ ManualDelete
          │                            │
          │  ┌──────────────┐          │
          └─▶│ PendingDelete│──────────┘
             └──────────────┘
               │          ▲
          Timed/    ContainerStarted
          Forever        │
               │         │
               ▼         │
             ┌──────────────┐
             │  Retaining   │
             └──────────────┘
```

**Transition entry point**:

```go
func (sm *Manager) Transition(containerID, serviceName string, event TransitionEvent) ([]Action, error)
```

`TransitionEvent`: `ContainerStarted`, `ContainerStopped`, `RetentionExpired`, `CleanupComplete`.

**Each Transition**:
1. Updates entry status.
2. Marks dirty (triggers persistence).
3. Returns action list for Controller to execute.

**Key rule**: `Transition()` does NOT call Cloudflare API — pure state computation. Controller executes actions and handles failures (hook point for Phase 4 compensation queue).

### 2.2 Retention Behavior Matrix

**On container stop**:

| Policy | Ingress | DNS | State | Timer |
|--------|---------|-----|-------|-------|
| `Immediate` | Delete | Delete | → `PendingDelete` → `Deleted` | None |
| `Timed` | Keep | Keep | → `Retaining`, store `DeletedAt` | Counted via GC ticker |
| `Forever` | Keep | Keep | → `Retaining`, no expiry | None |

**On container restart during retention (all policies)**:

| Action | Behavior |
|--------|----------|
| Ingress | Re-register (if labels changed) |
| DNS | Sync update |
| State | `Retaining` → `Active` |

**On retention expiry (Timed only)**:

| Layer | Action |
|-------|--------|
| Ingress | Delete hostname rule |
| DNS | Delete CNAME record |
| State | `Retaining` → `PendingDelete` → `Deleted` |

**After process restart**:

| Policy | Recovery |
|--------|----------|
| `Immediate` | Nothing to recover (already deleted) |
| `Timed` | Recalculate remaining time from persisted `DeletedAt`; if expired, clean up immediately |
| `Forever` | Restore as `Retaining`, await container restart or manual cleanup |

### 2.3 Timer Management

No per-entry goroutine timers. Reuse GC ticker (60s):

```
GC ticker → RunGC → iterate Retaining entries → check Timed expiry → Transition(RetentionExpired)
```

Maximum detection latency: 60s (acceptable given retention durations are minutes/hours).

### 2.4 Phase 1 → Phase 2 Migration

Phase 1's `Reconcile()` is upgraded internally:

```
Phase 1: Reconcile() → compute desired ingress → diff → apply
Phase 2: Reconcile() → iterate state machine entries → Transition() → execute actions
```

Same interface, upgraded implementation. Phase 1 infrastructure (ticker, debounce, feature flag) fully reused.

### 2.5 Testing & Acceptance

- Cover all three retention policies × all lifecycle stages (stop/restart/expiry/process restart).
- No "Ingress deleted but DNS remains" or reverse inconsistency.
- `Forever` entries never auto-cleaned.

### 2.6 Risks & Mitigation

| Risk | Mitigation |
|------|-----------|
| Behavior change surprises users | Version notes + migration guide + optional compat mode |
| State machine bugs | Pure-function logic, easily unit-tested |
| Dual path during migration | Old path kept behind flag until Phase 2 validated |

---

## Phase 3 (P1, 1-2 weeks): State Model Upgrade — Per-Service Keys

**Goal**: State primary key upgraded from `containerID` to `containerID + serviceName`.

**Key changes**:
- `activeTunnels` key format unified with `pendingDeletes` (`containerID:serviceName` compound key).
- `containerRules` (`containerID → []hostname`) retained for aggregate queries.
- State file version: 1 → 2. One-time migration on load: split single entries into per-service entries.
- Migration failure: degrade to empty state + log alarm, don't block startup.

**Depends on**: Phase 2 state machine. Phase 3 only changes key granularity, not transition logic.

---

## Phase 4 (P1, 1 week): Failure Compensation & Retry Governance

**Goal**: Cloudflare API failures observable, recoverable, eventually consistent.

**Key changes**:
- Error taxonomy: `Retryable` (429/5xx/timeout) vs `Permanent` (401/403/bad params). Existing retry in `cloudflareManager` is layer 1.
- Compensation queue (layer 2): persisted in state. Each record: `{targetState, attemptedActions, retryCount, lastError, nextRetryAt}`.
- Background goroutine replays with exponential backoff. Success → dequeue. Over limit → dead letter (log, no infinite retry).
- **Integration point**: Phase 2's `Transition()` action execution layer — Controller executes actions, failures enqueue for retry.

---

## Phase 5 (P1/P2, 1 week): Persistence Closed Loop

**Goal**: State continuity across restarts; retention/flapping info not lost.

**Key changes**:
- Three-layer save, all triggered via `markDirty()`:
  - **Periodic**: existing `SaveIfDirty()` in GC ticker (already implemented).
  - **Critical events**: `Transition()` marks dirty; saved on next ticker cycle (no immediate disk write to avoid high-frequency IO).
  - **Pre-exit**: `ForceSave()` (already implemented).
- Atomic write: existing tmp+rename mechanism sufficient.
- Backup rotation: keep last 3 backups (`.bak.1`, `.bak.2`, `.bak.3`). Fallback on corruption.
- Startup validation: check version, required fields, timestamp sanity. Validation failure → degrade to empty state + alarm log.

---

## Phase 6 (P2, ongoing): Observability & Operations

**Goal**: Reduce time-to-diagnose; close "discover → locate → fix" loop.

**Key changes**:
- **Structured log fields**: `container_id`, `service_name`, `hostname`, `action`, `result` — across event handling, reconcile, GC.
- **Metrics** (Prometheus `/metrics` endpoint):
  - `docktunnel_events_total{type, result}`
  - `docktunnel_reconcile_duration_seconds`
  - `docktunnel_dns_sync_operations{operation, result}`
  - `docktunnel_retention_entries{status}`
  - `docktunnel_compensation_queue_length`
- **Diagnostics endpoint**: `/debug/state` returns `{desired_state, actual_state, diff}` for drift comparison.
- Metrics laid incrementally from Phase 1 onward, not all at the end.

---

## Cross-Cutting: Common Pattern

Phases 2-6 all build on the same foundation:

- **State machine** defines "what should be".
- **Actions** define "what to do".
- Later phases extend these concepts: adjust granularity (Phase 3), handle failure (Phase 4), persist (Phase 5), expose (Phase 6).

## Execution Order

1. Phase 1 + P0 tests first.
2. Phase 2 (state machine) before Phase 3 (key model) — avoid rework.
3. Phase 4 and Phase 5 can proceed in parallel after Phase 2.
4. Phase 6 starts laying groundwork from Phase 1, not deferred to the end.
