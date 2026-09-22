# Controller Phase 2: Split God Object Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split `internal/controller/controller.go` (1244 lines) into 7 focused helper-struct files in the same package, with `Controller` becoming an orchestrator that holds helper struct pointers. Delete dead `state.Manager` flapping code as a side-effect.

**Architecture:** Bidirectional references — each helper struct holds a `*Controller` pointer; methods access `c.X` directly. No public API changes; `Controller` keeps delegating methods for every externally-called method. Pure relocation, not decoupling.

**Tech Stack:** Go 1.24, `sync.RWMutex`, `log/slog`, existing Cloudflare/Docker SDKs. No new dependencies.

**Branch:** `feature/sync-worker` (continuing).

---

## File Structure (target)

After Phase 2 complete:

| File | Responsibility |
|---|---|
| `controller.go` | `Controller` struct, `NewController`, lifecycle (`Start`/`StopSyncWorker`), `CleanupResources`, and all public delegating methods |
| `dispatcher.go` | Event routing: `Dispatch`, `dispatchInner`, `isDocktunnelEnabled` |
| `handlers.go` | Container lifecycle handlers + label parsing glue |
| `syncer.go` | Cloudflare write path + actual-state cache |
| `reconciler.go` | Drift detection + periodic reconcile |
| `health.go` | Container flapping detection + `ContainerHealth` type |
| `gc.go` | Retention GC + retention gauge updater |
| `compensation.go` | Compensation queue glue |
| `diagnostics.go` | `/debug/state` response builder |
| `sync_worker.go` | Unchanged from Phase 1 |
| `validator.go` | Unchanged |

Each helper struct is **unexported** (lowercase type name). Controller holds an unexported pointer field. External callers see only `Controller` methods.

---

## Critical Constraints

These apply to every task. Each commit MUST satisfy:

1. `go test ./... -count=1` — green
2. `go vet ./...` — silent
3. `gofmt -s -l .` — empty output
4. `controller_test.go` (997 lines) — **untouched**. If a test fails, the refactor was wrong.
5. No public API changes: every exported `Controller` method that exists today must exist after the task with the same signature.
6. Method bodies are copied **verbatim** unless the task explicitly says otherwise (only Task 5 changes a body, to drop a `c.mu` lock around a health call).
7. Use `git add -f` for any file under `docs/superpowers/` (matches `.gitignore` pattern from prior commits).

---

## Task 1: Extract `dispatcher`

**Files:**
- Create: `internal/controller/dispatcher.go`
- Modify: `internal/controller/controller.go`

`Dispatch` currently lives on `Controller` and records metrics then calls `dispatchInner`. Both move to `dispatcher`; `Controller.Dispatch` becomes a delegating one-liner. `isDocktunnelEnabled` (a private helper called only by handlers) moves to `dispatcher` for cohesion with `dispatchInner` — but `handlers.go` will need access in later tasks, so keep a `Controller.isDocktunnelEnabled` delegator until handlers land.

- [ ] **Step 1: Create `internal/controller/dispatcher.go`**

```go
package controller

import (
	"context"

	"docktunnel/internal/events"
	"docktunnel/internal/metrics"
	eventTypes "github.com/docker/docker/api/types/events"
)

// dispatcher routes Docker events to the appropriate handler. Stateless;
// holds a Controller pointer for handler dispatch.
type dispatcher struct {
	c *Controller
}

func newDispatcher(c *Controller) *dispatcher {
	return &dispatcher{c: c}
}

// Dispatch is the unified entry point for all event processing.
func (d *dispatcher) Dispatch(ctx context.Context, event events.Event) error {
	err := d.dispatchInner(ctx, event)

	result := metrics.ResultSuccess
	if err != nil {
		result = metrics.ResultFailure
	}
	metrics.RecordEvent(string(event.Type), result)

	return err
}

// dispatchInner is the original dispatch switch.
func (d *dispatcher) dispatchInner(ctx context.Context, event events.Event) error {
	switch event.Type {
	case eventTypes.ActionStart:
		return d.c.handleContainerStart(ctx, event)
	case eventTypes.ActionStop:
		return d.c.handleContainerStop(ctx, event)
	case eventTypes.ActionDie:
		return d.c.handleContainerStop(ctx, event)
	case events.ActionHealthHealthy:
		return d.c.handleHealthHealthy(ctx, event)
	case events.ActionHealthUnhealthy:
		return d.c.handleHealthUnhealthy(ctx, event)
	case events.ActionHealthStarting:
		return d.c.handleHealthUnhealthy(ctx, event)
	case events.ActionResync:
		return d.c.handleResync(ctx)
	default:
		return nil
	}
}

// isDocktunnelEnabled returns true if the container has docktunnel.enable=true.
func (d *dispatcher) isDocktunnelEnabled(event events.Event) bool {
	if event.ContainerInfo == nil || event.ContainerInfo.Config == nil || event.ContainerInfo.Config.Labels == nil {
		return false
	}
	return event.ContainerInfo.Config.Labels["docktunnel.enable"] == "true"
}
```

- [ ] **Step 2: Modify `internal/controller/controller.go`**

Remove `Dispatch`, `dispatchInner`, `isDocktunnelEnabled` method bodies (lines roughly 148–219 in the current file). Add a `dispatcher *dispatcher` field on the `Controller` struct, initialize it in `NewController`, and add delegating methods.

In the `Controller` struct (around line 60–89), add:

```go
	// Internal helpers (Phase 2 extraction)
	dispatcher *dispatcher
```

In `NewController` (before `return controller`), add:

```go
	controller.dispatcher = newDispatcher(controller)
```

Replace the removed `Dispatch` method with:

```go
// Dispatch 是所有事件处理的统一入口
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
	return c.dispatcher.Dispatch(ctx, event)
}
```

Add a delegator for `isDocktunnelEnabled` (handlers still call `c.isDocktunnelEnabled` — Task 4 moves them, but for now `Controller.isDocktunnelEnabled` must still resolve):

```go
// isDocktunnelEnabled delegates to dispatcher. Temporary; will be inlined
// into handlers.go once handlers are extracted.
func (c *Controller) isDocktunnelEnabled(event events.Event) bool {
	return c.dispatcher.isDocktunnelEnabled(event)
}
```

Clean up unused imports in `controller.go` after the removals: `metrics` and `eventTypes` are no longer used directly. Run `goimports -w internal/controller/controller.go` (or `gofmt -s -w` then manually drop unused imports).

- [ ] **Step 3: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
gofmt -s -l .
```

Expected: all green, `gofmt -s -l` empty.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/dispatcher.go internal/controller/controller.go
git commit -m "refactor(controller): extract dispatcher"
```

---

## Task 2: Extract `reconciler`

**Files:**
- Create: `internal/controller/reconciler.go`
- Modify: `internal/controller/controller.go`

`Reconcile`, `handleResync`, `ReconcileEnabled`, `ReconcileInterval` move to `reconciler`. `reconcileEnabled` and `reconcileInterval` fields **stay on `Controller`** (shared config; multiple future helpers may want them).

- [ ] **Step 1: Create `internal/controller/reconciler.go`**

```go
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"docktunnel/internal/label"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// reconciler drifts current state against desired state on a periodic
// ticker. Stateless; operates on Controller's maps under c.mu.
type reconciler struct {
	c   *Controller
	log *slog.Logger
}

func newReconciler(c *Controller, log *slog.Logger) *reconciler {
	return &reconciler{c: c, log: log}
}

// handleResync performs a full state synchronization after Docker daemon reconnection.
func (r *reconciler) handleResync(ctx context.Context) error {
	r.log.Info("Handling resync event after Docker reconnection")
	return r.c.Sync(ctx)
}

// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
func (r *reconciler) Reconcile(ctx context.Context) error {
	if r.c.dockerManager == nil {
		return nil
	}

	tunnel := r.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	eventsList, err := r.c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}

	desiredRules := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	desiredContainerRules := make(map[string][]string)

	for _, event := range eventsList {
		if !r.c.isDocktunnelEnabled(event) {
			continue
		}
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse labels during reconcile",
				"action", "parse_labels",
				"result", "failure",
				"containerID", event.ContainerID,
				"error", err)
			continue
		}

		var hostnames []string
		for _, rule := range parsedRules {
			if rule.Hostname.Value != "" {
				desiredRules[rule.Hostname.Value] = *rule
				hostnames = append(hostnames, rule.Hostname.Value)
			}
		}
		if len(hostnames) > 0 {
			desiredContainerRules[event.ContainerID] = hostnames
		}
	}

	// Preserve retaining rules (in ingressRules but not in containerRules)
	r.c.mu.RLock()
	currentContainerHostnames := make(map[string]bool)
	for _, hostnames := range r.c.containerRules {
		for _, h := range hostnames {
			currentContainerHostnames[h] = true
		}
	}
	for hostname, rule := range r.c.ingressRules {
		if !currentContainerHostnames[hostname] {
			if _, inDesired := desiredRules[hostname]; !inDesired {
				desiredRules[hostname] = rule
			}
		}
	}
	r.c.mu.RUnlock()

	// Diff desired vs current
	r.c.mu.RLock()
	hasDiff := len(desiredRules) != len(r.c.ingressRules)
	if !hasDiff {
		for hostname, desiredRule := range desiredRules {
			existing, exists := r.c.ingressRules[hostname]
			if !exists || existing.Service.Value != desiredRule.Service.Value {
				hasDiff = true
				break
			}
		}
	}
	r.c.mu.RUnlock()

	if !hasDiff {
		slog.Debug("Reconcile: no drift detected")
		return nil
	}

	slog.Info("Reconcile: drift detected, syncing",
		"current_rules", len(r.c.ingressRules),
		"desired_rules", len(desiredRules))

	r.c.mu.Lock()
	r.c.ingressRules = desiredRules
	r.c.containerRules = desiredContainerRules
	r.c.mu.Unlock()

	if err := r.c.syncToCloudflare(ctx); err != nil {
		return err
	}

	r.c.syncer.refreshActualState(ctx)

	return nil
}

// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (r *reconciler) ReconcileEnabled() bool {
	r.c.mu.RLock()
	defer r.c.mu.RUnlock()
	return r.c.reconcileEnabled
}

// ReconcileInterval returns the reconciliation interval.
func (r *reconciler) ReconcileInterval() time.Duration {
	r.c.mu.RLock()
	defer r.c.mu.RUnlock()
	return r.c.reconcileInterval
}
```

Note: `strings` import is currently unused in the snippet — drop if `go vet` complains. The original `Reconcile` does not use `strings`; this is a typo in the plan, drop it.

- [ ] **Step 2: Modify `internal/controller/controller.go`**

Remove `Reconcile`, `handleResync`, `ReconcileEnabled`, `ReconcileInterval` method bodies. Add `reconciler *reconciler` field to `Controller`, initialize in `NewController`, and add delegating methods.

Add to `Controller` struct:

```go
	reconciler *reconciler
```

In `NewController` (after `controller.dispatcher = newDispatcher(controller)`):

```go
	controller.reconciler = newReconciler(controller, slog.Default())
```

Add delegating methods:

```go
// handleResync performs a full state synchronization after Docker daemon reconnection.
func (c *Controller) handleResync(ctx context.Context) error {
	return c.reconciler.handleResync(ctx)
}

// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
func (c *Controller) Reconcile(ctx context.Context) error {
	return c.reconciler.Reconcile(ctx)
}

// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (c *Controller) ReconcileEnabled() bool {
	return c.reconciler.ReconcileEnabled()
}

// ReconcileInterval returns the reconciliation interval.
func (c *Controller) ReconcileInterval() time.Duration {
	return c.reconciler.ReconcileInterval()
}
```

Drop unused imports from `controller.go` after the move (`label` and `zero_trust` likely become unused if no other method needs them — verify with `go build`).

- [ ] **Step 3: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
gofmt -s -l .
```

Expected: green.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/reconciler.go internal/controller/controller.go
git commit -m "refactor(controller): extract reconciler"
```

---

## Task 3: Extract `gc`, `compensation`, `diagnostics` shims (single commit)

**Files:**
- Create: `internal/controller/gc.go`
- Create: `internal/controller/compensation.go`
- Create: `internal/controller/diagnostics.go`
- Modify: `internal/controller/controller.go`

These are small stateless shims — combine into one commit. `gc` covers `RunGarbageCollection` + `updateRetentionGauge`. `compensation` covers `ExecuteAction` + `RunCompensationLoop` + 3 setters. `diagnostics` covers `GetDebugState` + `snapshotRuleViewsLocked`. `refreshActualState` and `setLastKnownActualRules` move with `syncer` in Task 4 (they touch `actualStateMu`, which moves with the syncer); for now leave them on `Controller` and add temporary delegators if needed.

- [ ] **Step 1: Create `internal/controller/gc.go`**

```go
package controller

import (
	"context"
	"fmt"
	"log/slog"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
)

// gc runs retention garbage collection. Stateless shim.
type gc struct {
	c   *Controller
	log *slog.Logger
}

func newGC(c *Controller, log *slog.Logger) *gc {
	return &gc{c: c, log: log}
}

// RunGarbageCollection runs garbage collection for expired retention policies (T062, T063)
func (g *gc) RunGarbageCollection(ctx context.Context) error {
	expiredEntries, err := g.c.stateManager.RunGC(ctx)
	if err != nil {
		return fmt.Errorf("state manager GC failed: %w", err)
	}

	if len(expiredEntries) == 0 {
		g.c.gc.updateRetentionGauge()
		return nil
	}

	slog.Info("Garbage collection found expired entries", "count", len(expiredEntries))

	removed := 0
	g.c.mu.Lock()
	for _, entry := range expiredEntries {
		hostname := entry.Config.Hostname
		if hostname != "" {
			if _, exists := g.c.ingressRules[hostname]; exists {
				delete(g.c.ingressRules, hostname)
				removed++
				slog.Info("Removed expired route from ingress rules",
					"container_id", entry.ContainerID,
					"hostname", hostname)
			}
		}
	}
	g.c.mu.Unlock()

	if removed > 0 {
		metrics.AddGCDeletions(removed)
	}

	if err := g.c.syncToCloudflare(ctx); err != nil {
		return fmt.Errorf("failed to sync after GC: %w", err)
	}

	slog.Info("Garbage collection completed successfully",
		"expired_count", len(expiredEntries))

	g.c.gc.updateRetentionGauge()

	return nil
}

// updateRetentionGauge counts entries by lifecycle status and updates
// the docktunnel_retention_entries gauge.
func (g *gc) updateRetentionGauge() {
	snapshot := g.c.stateManager.GetSnapshot()
	counts := map[string]int{
		"Active":        0,
		"Retaining":     0,
		"PendingDelete": 0,
	}
	for _, entry := range snapshot.ActiveTunnels {
		if entry.Status == types.StatusActive {
			counts["Active"]++
		}
	}
	for _, entry := range snapshot.PendingDeletions {
		switch entry.Status {
		case types.StatusRetaining:
			counts["Retaining"]++
		case types.StatusPendingDelete:
			counts["PendingDelete"]++
		}
	}
	for status, n := range counts {
		metrics.SetRetentionEntries(status, n)
	}
}
```

Note: `g.c.gc.updateRetentionGauge()` is a self-reference — fine, since by the time the field exists, the pointer is set. But cleaner: rename the receiver calls to `g.updateRetentionGauge()` (same struct). Use `g.` not `g.c.gc.`:

```go
	// In RunGarbageCollection, replace g.c.gc.updateRetentionGauge() with:
	g.updateRetentionGauge()
```

Fix that before committing.

- [ ] **Step 2: Create `internal/controller/compensation.go`**

```go
package controller

import (
	"context"
	"log/slog"
	"time"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
)

// compensation drives the failed-action retry queue. Stateless shim.
type compensation struct {
	c   *Controller
	log *slog.Logger
}

func newCompensation(c *Controller, log *slog.Logger) *compensation {
	return &compensation{c: c, log: log}
}

// ExecuteAction executes a single action (e.g., delete route and sync).
// Used by the compensation queue to retry failed actions.
//
// Race guard: before deleting the ingress rule for a hostname, check whether
// the rule is still registered. If a stop event enqueued this delete and the
// container then restarted (handleContainerStart re-added the same hostname),
// the queued delete would otherwise fire after the container is back up and
// remove the live route. Skipping the delete when the rule is present lets
// the start path win.
func (comp *compensation) ExecuteAction(ctx context.Context, action types.Action) error {
	if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
		comp.c.mu.Lock()
		_, stillRegistered := comp.c.ingressRules[action.Hostname]
		if stillRegistered {
			comp.c.mu.Unlock()
			slog.Info("Skipping compensation delete: hostname is currently registered (container likely restarted)",
				"action", action.Kind,
				"hostname", action.Hostname,
				"container_id", action.ContainerID,
			)
			return nil
		}
		delete(comp.c.ingressRules, action.Hostname)
		comp.c.mu.Unlock()
	}
	return comp.c.syncToCloudflare(ctx)
}

// RunCompensationLoop starts the compensation queue background loop.
// Blocks until ctx is cancelled.
func (comp *compensation) RunCompensationLoop(ctx context.Context) {
	executor := func(action types.Action) error {
		return comp.c.ExecuteAction(ctx, action)
	}

	gaugeDone := make(chan struct{})
	go func() {
		defer close(gaugeDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pending := comp.c.stateManager.GetAllPendingActions()
				metrics.SetCompensationQueueLength(len(pending))
			case <-ctx.Done():
				return
			}
		}
	}()

	comp.c.stateManager.RunCompensation(ctx, executor)
	<-gaugeDone
}

// SetCompensationConfig configures the compensation queue parameters.
func (comp *compensation) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	comp.c.stateManager.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}

// SetCompensationQueueCap sets the maximum compensation queue size.
func (comp *compensation) SetCompensationQueueCap(size int) {
	comp.c.stateManager.SetCompensationQueueCap(size)
}

// SetPersistenceConfig configures backup and validation settings.
func (comp *compensation) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	comp.c.stateManager.SetBackupCount(backupCount)
	comp.c.stateManager.SetValidateOnLoad(validateOnLoad)
}
```

Note: `comp.c.ExecuteAction` in `RunCompensationLoop`'s executor refers to the Controller-level delegator (added in Step 5). This is intentional — `Controller.ExecuteAction` is the public API; the executor binds to it for consistency with existing tests.

- [ ] **Step 3: Create `internal/controller/diagnostics.go`**

```go
package controller

import (
	"time"

	"docktunnel/internal/diagnostics"
)

// diagnostics serves the /debug/state endpoint. Stateless; reads from
// Controller's desired state and the actual-state cache (owned by syncer
// after Task 4; for now, still on Controller).
type diagnosticsHelper struct {
	c *Controller
}

func newDiagnosticsHelper(c *Controller) *diagnosticsHelper {
	return &diagnosticsHelper{c: c}
}

// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
func (d *diagnosticsHelper) GetDebugState() diagnostics.DebugStateResponse {
	d.c.actualStateMu.RLock()
	defer d.c.actualStateMu.RUnlock()

	desired := d.c.snapshotRuleViewsLocked()
	actual := d.c.lastKnownActualRules
	source := "empty"
	if actual != nil {
		source = "live_cache"
	}

	return diagnostics.DebugStateResponse{
		Timestamp:    time.Now(),
		DesiredState: desired,
		ActualState:  actual,
		Source:       source,
		Diff:         diagnostics.ComputeDiff(desired, actual),
	}
}

// snapshotRuleViewsLocked builds a slice of RuleView from c.ingressRules.
// Caller must hold c.mu (read or write).
func (d *diagnosticsHelper) snapshotRuleViewsLocked() []diagnostics.RuleView {
	views := make([]diagnostics.RuleView, 0, len(d.c.ingressRules))
	for _, rule := range d.c.ingressRules {
		views = append(views, diagnostics.RuleView{
			Hostname: rule.Hostname.Value,
			Service:  rule.Service.Value,
			Path:     rule.Path.Value,
		})
	}
	return views
}
```

Naming clash: the package is `controller`, the helper is `diagnosticsHelper` (not `diagnostics`, which clashes with the import). Use `diagnosticsHelper` consistently.

- [ ] **Step 4: Modify `internal/controller/controller.go`**

Remove `RunGarbageCollection`, `updateRetentionGauge`, `ExecuteAction`, `RunCompensationLoop`, `SetCompensationConfig`, `SetCompensationQueueCap`, `SetPersistenceConfig`, `GetDebugState`, `snapshotRuleViewsLocked` method bodies.

Add fields to `Controller` struct:

```go
	gc             *gc
	compensation   *compensation
	diagnosticsHelper *diagnosticsHelper
```

Initialize in `NewController` (after `controller.reconciler = ...`):

```go
	controller.gc = newGC(controller, slog.Default())
	controller.compensation = newCompensation(controller, slog.Default())
	controller.diagnosticsHelper = newDiagnosticsHelper(controller)
```

Add delegating methods:

```go
// RunGarbageCollection runs garbage collection for expired retention policies.
func (c *Controller) RunGarbageCollection(ctx context.Context) error {
	return c.gc.RunGarbageCollection(ctx)
}

// ExecuteAction executes a single compensation action.
func (c *Controller) ExecuteAction(ctx context.Context, action types.Action) error {
	return c.compensation.ExecuteAction(ctx, action)
}

// RunCompensationLoop starts the compensation queue background loop.
func (c *Controller) RunCompensationLoop(ctx context.Context) {
	c.compensation.RunCompensationLoop(ctx)
}

// SetCompensationConfig configures the compensation queue parameters.
func (c *Controller) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	c.compensation.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}

// SetCompensationQueueCap sets the maximum compensation queue size.
func (c *Controller) SetCompensationQueueCap(size int) {
	c.compensation.SetCompensationQueueCap(size)
}

// SetPersistenceConfig configures backup and validation settings.
func (c *Controller) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	c.compensation.SetPersistenceConfig(backupCount, validateOnLoad)
}

// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
func (c *Controller) GetDebugState() diagnostics.DebugStateResponse {
	return c.diagnosticsHelper.GetDebugState()
}
```

Keep `Controller.snapshotRuleViewsLocked` as a temporary delegator (it's still called from `diagnosticsHelper` and from `refreshActualState` which stays on Controller until Task 4):

```go
// snapshotRuleViewsLocked delegates to diagnosticsHelper. Used by refreshActualState
// (still on Controller until syncer extraction).
func (c *Controller) snapshotRuleViewsLocked() []diagnostics.RuleView {
	return c.diagnosticsHelper.snapshotRuleViewsLocked()
}
```

- [ ] **Step 5: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
gofmt -s -l .
```

- [ ] **Step 6: Commit**

```bash
git add internal/controller/gc.go internal/controller/compensation.go internal/controller/diagnostics.go internal/controller/controller.go
git commit -m "refactor(controller): extract gc, compensation, diagnostics shims"
```

---

## Task 4: Extract `syncer`

**Files:**
- Create: `internal/controller/syncer.go`
- Modify: `internal/controller/controller.go`

The largest extraction. `syncer` owns `actualStateMu` + `lastKnownActualRules` (moves out of `Controller`). `Sync`, `performSync`, `syncDNSRecords`, `syncToCloudflare`, `GetIngressRules`, `refreshActualState`, `setLastKnownActualRules` move to `syncer`. `CleanupResources` (stays on Controller) gets rewritten to call `c.syncer.FlushSync(ctx)`, which delegates to `c.syncWorker.FlushSync(ctx)`.

- [ ] **Step 1: Create `internal/controller/syncer.go`**

```go
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/diagnostics"
	"docktunnel/internal/label"
	"docktunnel/internal/metrics"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// syncer drives Cloudflare writes through the syncWorker and owns the
// actual-state cache used by /debug/state.
type syncer struct {
	c   *Controller
	log *slog.Logger

	actualStateMu        sync.RWMutex
	lastKnownActualRules []diagnostics.RuleView
}

func newSyncer(c *Controller, log *slog.Logger) *syncer {
	return &syncer{c: c, log: log}
}

// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (s *syncer) Sync(ctx context.Context) error {
	// [copy Sync body verbatim from Controller.Sync, replacing c.X with s.c.X
	//  and any direct reference to actualStateMu/lastKnownActualRules with
	//  s.actualStateMu / s.lastKnownActualRules]
	// At the bottom of Sync, replace `c.refreshActualState(ctx)` with `s.refreshActualState(ctx)`.
	// (Full body omitted here for plan length; copy from controller.go lines 538–684.)
}

// syncToCloudflare signals the sync worker that a sync is desired.
func (s *syncer) syncToCloudflare(_ context.Context) error {
	s.c.syncWorker.TriggerSync()
	return nil
}

// FlushSync blocks until the worker has processed one sync cycle triggered by this call.
func (s *syncer) FlushSync(ctx context.Context) error {
	return s.c.syncWorker.FlushSync(ctx)
}

// performSync 执行实际的Cloudflare同步操作
func (s *syncer) performSync(ctx context.Context) error {
	// [copy performSync body verbatim, c.X → s.c.X, c.GetIngressRules() → s.GetIngressRules(),
	//  c.syncDNSRecords(ctx) → s.syncDNSRecords(ctx)]
}

// syncDNSRecords 同步DNS记录到Cloudflare
func (s *syncer) syncDNSRecords(ctx context.Context) error {
	// [copy body verbatim, c.X → s.c.X]
}

// GetIngressRules 获取当前的Ingress规则
func (s *syncer) GetIngressRules() []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()

	rules := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(s.c.ingressRules)+1)
	for _, rule := range s.c.ingressRules {
		rules = append(rules, rule)
	}
	rules = append(rules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http_status:404"),
	})
	return rules
}

// refreshActualState fetches the live tunnel config from Cloudflare and
// caches it for /debug/state.
func (s *syncer) refreshActualState(ctx context.Context) {
	ingress, err := s.c.cloudflareManager.GetConfiguration(ctx)
	if err != nil {
		slog.Warn("Failed to fetch live tunnel config for diagnostics",
			"error", err)
		return
	}

	views := make([]diagnostics.RuleView, 0, len(ingress))
	for _, rule := range ingress {
		if rule.Hostname == "" {
			continue
		}
		views = append(views, diagnostics.RuleView{
			Hostname: rule.Hostname,
			Service:  rule.Service,
			Path:     rule.Path,
		})
	}
	s.setLastKnownActualRules(views)
}

// setLastKnownActualRules replaces the cached actual-state snapshot.
func (s *syncer) setLastKnownActualRules(rules []diagnostics.RuleView) {
	s.actualStateMu.Lock()
	s.lastKnownActualRules = rules
	s.actualStateMu.Unlock()
}

// snapshotRuleViewsLocked delegates to diagnosticsHelper.
// Caller must hold c.mu.
func (s *syncer) snapshotRuleViewsLocked() []diagnostics.RuleView {
	return s.c.diagnosticsHelper.snapshotRuleViewsLocked()
}
```

**Important:** the `[copy ... verbatim]` markers above are NOT placeholders in the implementation sense — they are instruction to the implementer to copy the exact method body from the current `controller.go`. Each body is fully present in the spec's source file (`internal/controller/controller.go`) and must be ported line-by-line with `c.` → `s.c.` substitution. Show the subagent the current file; it copies the bodies.

- [ ] **Step 2: Modify `internal/controller/controller.go`**

Remove `Sync`, `syncToCloudflare`, `performSync`, `syncDNSRecords`, `GetIngressRules`, `refreshActualState`, `setLastKnownActualRules`, `snapshotRuleViewsLocked` method bodies from `Controller`.

Remove these fields from `Controller` struct (they move to `syncer`):

```go
	actualStateMu        sync.RWMutex
	lastKnownActualRules []diagnostics.RuleView
```

Add `syncer *syncer` field to `Controller` struct.

**Critical: rewire `syncWorker.syncFn`**. In `NewController`, the current code is:

```go
	controller.syncWorker = newSyncWorker(controller.debounceDuration, controller.performSync, slog.Default())
```

This must become:

```go
	controller.syncer = newSyncer(controller, slog.Default())
	controller.syncWorker = newSyncWorker(controller.debounceDuration, controller.syncer.performSync, slog.Default())
```

(Order matters: syncer must be constructed before syncWorker so the closure captures the right receiver. Currently the closure captures `controller` and calls `controller.performSync`; after extraction `performSync` lives on `syncer`, so the closure must call `controller.syncer.performSync`. Use the latter form to be explicit.)

Add delegating methods to `Controller`:

```go
// Sync 同步Docker容器状态到Cloudflare Tunnel配置
func (c *Controller) Sync(ctx context.Context) error {
	return c.syncer.Sync(ctx)
}

// GetIngressRules 获取当前的Ingress规则
func (c *Controller) GetIngressRules() []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	return c.syncer.GetIngressRules()
}

// syncToCloudflare signals the sync worker.
func (c *Controller) syncToCloudflare(ctx context.Context) error {
	return c.syncer.syncToCloudflare(ctx)
}

// refreshActualState delegates to syncer.
func (c *Controller) refreshActualState(ctx context.Context) {
	c.syncer.refreshActualState(ctx)
}
```

Rewrite `CleanupResources` (currently at controller.go lines 184–208) to use the syncer:

```go
// CleanupResources 清理创建的DNS记录
func (c *Controller) CleanupResources(ctx context.Context) error {
	c.mu.Lock()
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	c.containerRules = make(map[string][]string)
	c.mu.Unlock()

	slog.Info("Cleaning up resources: cleared internal rules")

	if err := c.syncer.FlushSync(ctx); err != nil {
		slog.Error("Failed to perform cleanup sync", "error", err, "result", "failure")
		return err
	}

	slog.Info("Resource cleanup completed successfully")
	return nil
}
```

Remove the `snapshotRuleViewsLocked` delegator added in Task 3 if it's no longer called from `Controller` directly. (After this task, only `diagnosticsHelper.snapshotRuleViewsLocked` is called, and `diagnosticsHelper` reads `c.ingressRules` directly — no Controller-level delegator needed.)

- [ ] **Step 3: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
gofmt -s -l .
```

Run specifically:

```bash
go test ./internal/controller/ -run TestCleanupResources_BlocksUntilSyncRun -count=1 -v
```

Expected: PASS, completes within ~2s (the blocking mock test from Phase 1).

- [ ] **Step 4: Commit**

```bash
git add internal/controller/syncer.go internal/controller/controller.go
git commit -m "refactor(controller): extract syncer"
```

---

## Task 5: Extract `healthTracker`

**Files:**
- Create: `internal/controller/health.go`
- Modify: `internal/controller/controller.go`
- Modify: `internal/controller/handlers.go` (handlers haven't been extracted yet — they still live in `controller.go`. This task moves health first, handlers come last in Task 6.)

This is the only task that changes call-site lock semantics. `containerHealth` + 4 flapping config fields move out of `Controller` into `healthTracker`, which owns its own mutex. Handler call sites currently under `c.mu.Lock()` must drop the lock around health calls.

- [ ] **Step 1: Create `internal/controller/health.go`**

```go
package controller

import (
	"log/slog"
	"sync"
	"time"
)

// ContainerHealth 记录容器的健康状态信息
type ContainerHealth struct {
	RestartCount int
	LastRestart  time.Time
	IsFlapping   bool
	CoolingUntil time.Time
}

// healthTracker detects container flapping and applies cooling periods.
// Owns its own mutex separate from Controller.mu to avoid lock-ordering
// constraints between handler-driven state mutations and health checks.
type healthTracker struct {
	c   *Controller
	log *slog.Logger

	mu              sync.Mutex
	containerHealth map[string]*ContainerHealth

	flappingWindow    time.Duration
	flappingThreshold int
	coolingPeriod     time.Duration
	maxCoolingPeriod  time.Duration
}

func newHealthTracker(c *Controller, log *slog.Logger, opts ControllerOptions) *healthTracker {
	h := &healthTracker{
		c:                 c,
		log:               log,
		containerHealth:   make(map[string]*ContainerHealth),
		flappingWindow:    opts.FlappingWindow,
		flappingThreshold: opts.FlappingThreshold,
		coolingPeriod:     opts.CoolingPeriod,
		maxCoolingPeriod:  opts.MaxCoolingPeriod,
	}

	if h.flappingWindow == 0 {
		h.flappingWindow = 1 * time.Minute
	}
	if h.flappingThreshold == 0 {
		h.flappingThreshold = 5
	}
	if h.coolingPeriod == 0 {
		h.coolingPeriod = 5 * time.Minute
	}
	if h.maxCoolingPeriod == 0 {
		h.maxCoolingPeriod = 30 * time.Minute
	}

	return h
}

// isFlapping 检查容器是否处于抖动状态
func (h *healthTracker) isFlapping(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	health, exists := h.containerHealth[containerID]
	if !exists {
		return false
	}

	if health.IsFlapping && time.Now().Before(health.CoolingUntil) {
		return true
	}

	return false
}

// updateContainerHealth 更新容器健康状态
func (h *healthTracker) updateContainerHealth(containerID string, isStartEvent bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()

	health, exists := h.containerHealth[containerID]
	if !exists {
		health = &ContainerHealth{}
		h.containerHealth[containerID] = health
	}

	if isStartEvent {
		if now.Sub(health.LastRestart) <= h.flappingWindow {
			health.RestartCount++

			if health.RestartCount >= h.flappingThreshold {
				health.IsFlapping = true
				coolingMultiplier := 1 << uint(health.RestartCount-h.flappingThreshold)
				coolingDuration := min(time.Duration(coolingMultiplier)*h.coolingPeriod, h.maxCoolingPeriod)
				health.CoolingUntil = now.Add(coolingDuration)

				slog.Warn("Container marked as flapping",
					"containerID", containerID,
					"restartCount", health.RestartCount,
					"coolingUntil", health.CoolingUntil)
			}
		} else {
			health.RestartCount = 1
		}

		health.LastRestart = now
	} else {
		slog.Debug("Container stopped", "containerID", containerID)
	}

	if health.IsFlapping && now.After(health.CoolingUntil) {
		health.IsFlapping = false
		health.RestartCount = 0
		slog.Info("Container cooling period ended, flapping status reset", "containerID", containerID)
	}
}
```

Note: the original `updateContainerHealth` was called under `c.mu` held by the caller. Now it takes its own `h.mu`. Caller changes follow in Step 2.

- [ ] **Step 2: Modify `internal/controller/controller.go`**

Remove `containerHealth` field and 4 flapping config fields from `Controller` struct. Remove `ContainerHealth` type definition (moves to `health.go`). Remove `isFlapping` and `updateContainerHealth` methods.

Add `healthTracker *healthTracker` field to `Controller`.

In `NewController`, **remove** the four flapping-config defaults currently set on the controller:

```go
	// REMOVE these lines:
	if controller.flappingWindow == 0 { controller.flappingWindow = 1 * time.Minute }
	if controller.flappingThreshold == 0 { controller.flappingThreshold = 5 }
	if controller.coolingPeriod == 0 { controller.coolingPeriod = 5 * time.Minute }
	if controller.maxCoolingPeriod == 0 { controller.maxCoolingPeriod = 30 * time.Minute }
```

And remove the corresponding field assignments in the `&Controller{...}` struct literal:

```go
	// REMOVE these from the struct literal:
	flappingWindow:    opts.FlappingWindow,
	flappingThreshold: opts.FlappingThreshold,
	coolingPeriod:     opts.CoolingPeriod,
	maxCoolingPeriod:  opts.MaxCoolingPeriod,
```

(The `ContainerHealth` map init also moves out of the Controller literal.)

Add initialization (after `controller.syncer = newSyncer(...)` and before `controller.syncWorker = newSyncWorker(...)`):

```go
	controller.healthTracker = newHealthTracker(controller, slog.Default(), opts)
```

Add delegating methods on `Controller` (called from handlers, which still live on Controller until Task 6):

```go
// isFlapping delegates to healthTracker.
func (c *Controller) isFlapping(containerID string) bool {
	return c.healthTracker.isFlapping(containerID)
}

// updateContainerHealth delegates to healthTracker.
func (c *Controller) updateContainerHealth(containerID string, isStartEvent bool) {
	c.healthTracker.updateContainerHealth(containerID, isStartEvent)
}
```

**Update handler call sites that hold `c.mu` around `updateContainerHealth`:**

In `handleContainerStart` (currently lines 305–307 of controller.go):

Before:
```go
	c.mu.Lock()
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()
```

After:
```go
	c.updateContainerHealth(event.ContainerID, true)
```

(No lock needed — `healthTracker` locks itself. The surrounding `c.mu` was only held because the old `updateContainerHealth` didn't have its own lock.)

In `handleContainerStop` (currently lines 443–451 of controller.go):

Before:
```go
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()
```

After:
```go
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.mu.Unlock()
	c.updateContainerHealth(event.ContainerID, false)
```

(Move the `updateContainerHealth` call outside the `c.mu` block — `healthTracker` no longer needs `c.mu` held.)

- [ ] **Step 3: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
gofmt -s -l .
```

If any test under `TestController_*Flapping*` or similar fails, the lock semantic change likely caused a race or assertion mismatch. Investigate before proceeding.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/health.go internal/controller/controller.go
git commit -m "refactor(controller): extract healthTracker"
```

---

## Task 6: Move handlers + remaining helpers into `handlers.go`

**Files:**
- Create: `internal/controller/handlers.go`
- Modify: `internal/controller/controller.go`

By this point `controller.go` should contain: struct definitions, `NewController`, lifecycle (`Start`/`StopSyncWorker`), `CleanupResources`, all delegating methods, and the four container handlers (`handleContainerStart`, `handleContainerStop`, `handleHealthHealthy`, `handleHealthUnhealthy`) plus `registerContainerRules` and `getServiceRetentionPolicy`. Move those six into `handlers.go` as `Controller` methods (they stay on `Controller` because they orchestrate multiple helpers — extraction to a separate struct would create a circular-feeling chain).

- [ ] **Step 1: Create `internal/controller/handlers.go`**

```go
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// handleContainerStart 处理容器启动事件
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	// [copy body verbatim from controller.go]
}

// handleContainerStop 处理容器停止事件
func (c *Controller) handleContainerStop(ctx context.Context, event events.Event) error {
	// [copy body verbatim]
}

// handleHealthHealthy re-exposes a container's services when it becomes healthy.
func (c *Controller) handleHealthHealthy(ctx context.Context, event events.Event) error {
	// [copy body verbatim]
}

// handleHealthUnhealthy removes a container's services when it becomes unhealthy.
func (c *Controller) handleHealthUnhealthy(ctx context.Context, event events.Event) error {
	// [copy body verbatim]
}

// registerContainerRules parses labels, validates, and registers ingress rules.
func (c *Controller) registerContainerRules(ctx context.Context, event events.Event) (map[string]string, error) {
	// [copy body verbatim]
}

// getServiceRetentionPolicy parses the retention policy for a specific service.
func (c *Controller) getServiceRetentionPolicy(labels map[string]string, serviceName string) types.RetentionPolicy {
	// [copy body verbatim]
}
```

The methods are still on `Controller` (not on a helper struct) — this file is just a relocation. No `c.X` → `s.c.X` rewriting needed.

- [ ] **Step 2: Modify `internal/controller/controller.go`**

Delete the bodies of those six methods. The Controller struct, NewController, lifecycle, CleanupResources, delegators, and `isDocktunnelEnabled` (still delegating to dispatcher from Task 1) remain.

Drop unused imports from `controller.go` after the moves: likely `events`, `label`, `types`, possibly `zero_trust` if only used by handler bodies. Verify with `go build`.

- [ ] **Step 3: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/controller/... -count=1
go test ./... -count=1
gofmt -s -l .
wc -l internal/controller/*.go
```

Expected: controller.go is now ~180 lines; new files are present.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/handlers.go internal/controller/controller.go
git commit -m "refactor(controller): move handlers into handlers.go"
```

---

## Task 7: Delete dead `state.Manager` flapping code

**Files:**
- Modify: `internal/state/manager.go`
- Modify: `internal/state/manager_test.go`
- Possibly modify: `pkg/types/tunnel.go`

- [ ] **Step 1: Confirm zero production callers**

```bash
grep -rn 'CheckFlapping\|RecordTransition\|MarkAsFlapping\|GetFlappingState' /workspace --include='*.go' | grep -v _test.go
```

Expected: only matches in `internal/state/manager.go` (the definitions themselves). If anything else matches, STOP — those callers need to be migrated first.

Also check `FlappingState` type usage:

```bash
grep -rn 'FlappingState' /workspace --include='*.go'
```

Expected: definition in `pkg/types/tunnel.go`; reads in `internal/state/manager.go` and `internal/state/manager_test.go`. If anything else, STOP.

- [ ] **Step 2: Delete from `internal/state/manager.go`**

Remove:
- Field `flappingStates` (and any related internal struct like `flappingState`)
- Methods: `CheckFlapping`, `RecordTransition`, `MarkAsFlapping`, `GetFlappingState`
- Initialization of `flappingStates` in `NewManager`

Read `manager.go` lines around the methods (currently lines 379–490 per earlier survey) and delete those line ranges. Verify by `grep CheckFlapping internal/state/manager.go` returns nothing.

- [ ] **Step 3: Delete from `internal/state/manager_test.go`**

Remove tests:
- `TestManager_CheckFlapping*`
- `TestManager_RecordTransition*`
- `TestManager_MarkAsFlapping*`
- `TestManager_GetFlappingState*`

Use `grep -n 'func Test.*Flapping\|func Test.*RecordTransition\|func Test.*MarkAsFlapping' internal/state/manager_test.go` to find them. Delete each test function.

- [ ] **Step 4: Optionally delete `FlappingState` type**

```bash
grep -rn 'FlappingState' /workspace --include='*.go'
```

If zero hits remain (after Step 2 and 3), delete the type from `pkg/types/tunnel.go`. If any hits remain, leave the type alone.

- [ ] **Step 5: Verify**

```bash
go build ./...
go vet ./...
go test ./internal/state/... -count=1
go test ./... -count=1
gofmt -s -l .
```

Expected: green. If state tests fail because some unrelated test referenced the deleted methods, that test needs review — it may have been a real caller (STOP and reassess).

- [ ] **Step 6: Commit**

```bash
git add internal/state/manager.go internal/state/manager_test.go pkg/types/tunnel.go
git commit -m "refactor(state): delete dead flapping detection code

state.Manager exposed CheckFlapping/RecordTransition/MarkAsFlapping/
GetFlappingState but no production caller invoked them — only their
own unit tests. The controller has its own healthTracker. Removing
the dead code path to reduce confusion about which flapping
implementation is authoritative."
```

---

## Task 8: Add minimal unit tests for extracted helpers

**Files:**
- Create: `internal/controller/health_test.go`
- Create: `internal/controller/syncer_test.go`

`controller_test.go` stays untouched. New tests prove the helpers construct cleanly and exercise one happy-path behavior each. They are NOT comprehensive — `controller_test.go` covers integration.

- [ ] **Step 1: Write `internal/controller/health_test.go`**

```go
package controller

import (
	"testing"
	"time"

	"docktunnel/internal/events"
)

func TestHealthTracker_isFlapping_NotFlappingByDefault(t *testing.T) {
	c := &Controller{}
	h := newHealthTracker(c, nil, ControllerOptions{
		FlappingWindow:    time.Minute,
		FlappingThreshold: 3,
		CoolingPeriod:     time.Minute,
		MaxCoolingPeriod:  5 * time.Minute,
	})

	if h.isFlapping("nonexistent") {
		t.Error("expected isFlapping=false for unknown container")
	}
}

func TestHealthTracker_updateContainerHealth_MarksFlappingAfterThreshold(t *testing.T) {
	c := &Controller{}
	h := newHealthTracker(c, nil, ControllerOptions{
		FlappingWindow:    time.Minute,
		FlappingThreshold: 3,
		CoolingPeriod:     time.Minute,
		MaxCoolingPeriod:  5 * time.Minute,
	})

	containerID := "test-container-1"
	// First two starts within the window don't trip the threshold.
	h.updateContainerHealth(containerID, true)
	h.updateContainerHealth(containerID, true)
	if h.isFlapping(containerID) {
		t.Error("expected isFlapping=false before threshold reached")
	}
	// Third start in the window crosses threshold (RestartCount=3).
	h.updateContainerHealth(containerID, true)
	if !h.isFlapping(containerID) {
		t.Error("expected isFlapping=true after threshold reached")
	}
}

func TestHealthTracker_defaultsAppliedWhenZero(t *testing.T) {
	c := &Controller{}
	h := newHealthTracker(c, nil, ControllerOptions{})

	if h.flappingWindow != time.Minute {
		t.Errorf("expected default flappingWindow=1m, got %v", h.flappingWindow)
	}
	if h.flappingThreshold != 5 {
		t.Errorf("expected default flappingThreshold=5, got %v", h.flappingThreshold)
	}
	if h.coolingPeriod != 5*time.Minute {
		t.Errorf("expected default coolingPeriod=5m, got %v", h.coolingPeriod)
	}
	if h.maxCoolingPeriod != 30*time.Minute {
		t.Errorf("expected default maxCoolingPeriod=30m, got %v", h.maxCoolingPeriod)
	}
}
```

Note: `events` import is unused in the snippet above — drop it. Only `testing` and `time` are needed.

- [ ] **Step 2: Write `internal/controller/syncer_test.go`**

```go
package controller

import (
	"testing"

	"docktunnel/internal/diagnostics"
)

func TestSyncer_setLastKnownActualRules_CachesForDebugState(t *testing.T) {
	c := &Controller{}
	s := newSyncer(c, nil)

	rules := []diagnostics.RuleView{
		{Hostname: "a.example.com", Service: "http://a:8080"},
		{Hostname: "b.example.com", Service: "http://b:8080"},
	}
	s.setLastKnownActualRules(rules)

	s.actualStateMu.RLock()
	got := s.lastKnownActualRules
	s.actualStateMu.RUnlock()

	if len(got) != 2 {
		t.Fatalf("expected 2 cached rules, got %d", len(got))
	}
	if got[0].Hostname != "a.example.com" {
		t.Errorf("expected first hostname a.example.com, got %s", got[0].Hostname)
	}
}

func TestSyncer_setLastKnownActualRules_NilEmptiesCache(t *testing.T) {
	c := &Controller{}
	s := newSyncer(c, nil)

	s.setLastKnownActualRules([]diagnostics.RuleView{{Hostname: "x"}})
	s.setLastKnownActualRules(nil)

	s.actualStateMu.RLock()
	got := s.lastKnownActualRules
	s.actualStateMu.RUnlock()

	if got != nil {
		t.Errorf("expected nil after setting nil, got %v", got)
	}
}
```

- [ ] **Step 3: Verify**

```bash
go test ./internal/controller/ -run 'TestHealthTracker|TestSyncer' -count=1 -v
go test ./... -count=1
go vet ./...
gofmt -s -l .
```

Expected: new tests pass; full suite still green.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/health_test.go internal/controller/syncer_test.go
git commit -m "test(controller): add healthTracker + syncer unit tests"
```

---

## Final verification

After all 8 tasks complete:

- [ ] **Step 1: Full test suite**

```bash
go test ./... -count=1
go vet ./...
gofmt -s -l .
```

All green, `gofmt` silent.

- [ ] **Step 2: Confirm controller.go is now small**

```bash
wc -l internal/controller/*.go
```

Expected: `controller.go` ~180–220 lines (down from 1244). Each new file roughly the size listed in the spec §2 table.

- [ ] **Step 3: Confirm no behavior regression in production paths**

Read `cmd/docktunnel/main.go` shutdown sequence and confirm `CleanupResources → syncer.FlushSync → syncWorker.FlushSync` chain still resolves correctly (verified in Task 4 Step 3 but re-check after all moves).

- [ ] **Step 4: Dispatch final whole-implementation review**

Use `superpowers:subagent-driven-development`'s final review step or dispatch directly:

```
Dispatch a code-reviewer subagent on `main..HEAD` for the controller
Phase 2 work. Spec at
docs/superpowers/specs/2026-06-19-controller-phase-2-design.md.
Verify: (1) controller_test.go untouched, (2) no behavior change,
(3) helper struct ownership per spec §3.1, (4) no public API change,
(5) lock semantics correct (healthTracker.mu separate from c.mu),
(6) dead flapping code fully removed, (7) syncWorker closure points
at syncer.performSync.
```

If review surfaces CRITICAL or IMPORTANT issues: address them per the subagent-driven-development workflow (implementer fixes → re-review). Do NOT call `superpowers:finishing-a-development-branch` until review is clean.

- [ ] **Step 5: Hand off to finishing-a-development-branch**

Once review passes, invoke `superpowers:finishing-a-development-branch`.
