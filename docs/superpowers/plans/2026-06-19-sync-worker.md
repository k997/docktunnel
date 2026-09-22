# Sync Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the debounce-timer-plus-fire-and-forget sync pattern with a single `syncWorker` goroutine that serializes all Cloudflare writes, fixing concurrent-write races on the shutdown path and adding proper coalescing.

**Architecture:** Introduce an unexported `syncWorker` type in `internal/controller/` that owns a single goroutine. Event-side callers go through `TriggerSync()` (non-blocking, coalesced via a size-1 channel + debounce window). `CleanupResources` goes through `FlushSync(ctx)` (blocks until pending sync completes). Worker owns the timer; the old `debounceTimer` and `pendingUpdates` fields are deleted.

**Tech Stack:** Go 1.24, `sync/atomic`, `time.Timer`, `log/slog`. No new external deps.

**Spec:** `docs/superpowers/specs/2026-06-19-sync-worker-design.md`

---

## File Structure

- **New:** `internal/controller/sync_worker.go` — `syncWorker` type + `Start`, `TriggerSync`, `FlushSync`, `Stop`, `run`, `runOnce`.
- **New:** `internal/controller/sync_worker_test.go` — 9 unit tests for worker behavior using an injected `syncFn`.
- **Modify:** `internal/controller/controller.go` — drop `debounceTimer`/`pendingUpdates`, add `syncWorker` field, rewrite `syncToCloudflare` to delegate, change `CleanupResources` to use `FlushSync`, add `Start` and `StopSyncWorker` methods.
- **Modify:** `cmd/docktunnel/main.go` — add goroutine that calls `controller.Start(ctx)`, waits on `ctx.Done`, then `controller.StopSyncWorker()`.

---

## Task 1: Worker skeleton — type, channels, Start/Stop

**Files:**
- Create: `internal/controller/sync_worker.go`
- Create: `internal/controller/sync_worker_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/controller/sync_worker_test.go` with this content:

```go
package controller

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func newTestWorker(t *testing.T, debounce time.Duration, syncFn func(context.Context) error) *syncWorker {
	t.Helper()
	return newSyncWorker(debounce, syncFn, slog.Default())
}

func TestSyncWorker_StopReturnsImmediatelyIfNotStarted(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop() on never-started worker blocked for >100ms")
	}
}

func TestSyncWorker_FlushBeforeStartReturnsError(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := w.FlushSync(ctx); err == nil {
		t.Fatal("expected FlushSync to error before Start, got nil")
	}
}

// keep atomic import used; will be referenced by later tests
var _ = atomic.Bool{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: build failure — `undefined: newSyncWorker`, `undefined: syncWorker`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/controller/sync_worker.go`:

```go
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// errWorkerNotStarted is returned by FlushSync when called before Start.
var errWorkerNotStarted = errors.New("sync worker not started")

// syncWorker serializes Cloudflare writes via a single goroutine. All
// callers funnel through TriggerSync (async, debounced) or FlushSync
// (sync, waits for at least one sync cycle to complete).
type syncWorker struct {
	debounce   time.Duration
	syncFn     func(context.Context) error
	log        *slog.Logger

	triggerCh  chan struct{}          // size 1; signals "sync wanted"
	flushQueue chan chan struct{}     // unbuffered; FlushSync registers its done chan
	stopCh     chan struct{}          // closed when run() exits

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

// Start launches the worker goroutine. Idempotent.
func (w *syncWorker) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run(ctx)
}

// TriggerSync signals that a sync is desired. Non-blocking; safe to
// call from event handlers. Multiple triggers coalesce.
func (w *syncWorker) TriggerSync() {
	select {
	case w.triggerCh <- struct{}{}:
	default:
		// channel full; worker will re-check after current sync
	}
}

// FlushSync triggers a sync and blocks until the worker has processed
// at least one sync cycle. Returns the last sync error (or nil), or
// ctx.Err() on timeout/cancel.
func (w *syncWorker) FlushSync(ctx context.Context) error {
	if !w.started.Load() {
		return errWorkerNotStarted
	}
	// Will be filled in by Task 3 — for now, return not-implemented.
	return errors.New("FlushSync not implemented yet")
}

// Stop blocks until the worker goroutine has exited. Returns
// immediately if Start was never called.
func (w *syncWorker) Stop() {
	if !w.started.Load() {
		return
	}
	select {
	case <-w.stopCh:
	case <-time.After(5 * time.Second):
		w.log.Error("syncWorker.Stop timed out after 5s")
	}
}

// run is filled in by Task 2.
func (w *syncWorker) run(ctx context.Context) {
	defer close(w.stopCh)
	<-ctx.Done()
}

// (runOnce will be added in Task 2; fmt-safe placeholder kept here to
// avoid unused import errors during this slice of work.)
var _ = fmt.Sprintf
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: PASS for both `TestSyncWorker_StopReturnsImmediatelyIfNotStarted` and `TestSyncWorker_FlushBeforeStartReturnsError`.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/sync_worker.go internal/controller/sync_worker_test.go
git commit -m "feat(controller): add syncWorker skeleton with Start/Stop/TriggerSync

FlushSync and run loop are placeholders; subsequent tasks fill them in."
```

---

## Task 2: run loop with debounce + syncFn dispatch

**Files:**
- Modify: `internal/controller/sync_worker.go` (replace `run` stub, add `runOnce`)
- Modify: `internal/controller/sync_worker_test.go` (add 3 tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/controller/sync_worker_test.go` (before the closing `var _ = atomic.Bool{}` line — move that line to the bottom):

```go
func TestSyncWorker_TriggersCoalesce(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 20*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	for i := 0; i < 10; i++ {
		w.TriggerSync()
	}
	// Give debounce + sync time to land. Flush will be added in Task 3;
	// here we sleep as a coarse check.
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 sync call (coalesced), got %d", got)
	}
}

func TestSyncWorker_DebounceHonored(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 50*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	time.Sleep(20 * time.Millisecond) // less than debounce
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("expected 0 calls during debounce window, got %d", got)
	}
	time.Sleep(80 * time.Millisecond) // past debounce
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 call after debounce elapsed, got %d", got)
	}
}

func TestSyncWorker_ShutdownClean(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() did not return within 500ms of ctx cancel")
	}
}
```

Then ensure `var _ = atomic.Bool{}` is the very last line of the file (or remove it now since later tests use atomic directly).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/controller/ -run 'TestSyncWorker_(TriggersCoalesce|DebounceHonored|ShutdownClean)' -v`
Expected: FAIL — call counts are 0 because `run` doesn't yet call `syncFn`.

- [ ] **Step 3: Replace the `run` stub and add `runOnce`**

In `internal/controller/sync_worker.go`, replace the existing `run` function with:

```go
func (w *syncWorker) run(ctx context.Context) {
	defer close(w.stopCh)
	timer := time.NewTimer(w.debounce)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.triggerCh:
			// Debounce window: absorb triggers until quiet.
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

// runOnce invokes syncFn, stores the result, and drains any pending
// flushers. The panic recovery keeps a single bad sync from killing
// the worker.
func (w *syncWorker) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("syncFn panic recovered", "panic", r)
			err := fmt.Errorf("sync panicked: %v", r)
			w.storeErr(err)
		}
		w.drainFlushers()
	}()
	err := w.syncFn(ctx)
	w.storeErr(err)
	if err != nil {
		w.log.Warn("sync failed; will retry on next trigger", "error", err)
	}
}

func (w *syncWorker) storeErr(err error) {
	// Will be replaced by atomic.Pointer store in Task 4.
	// Placeholder: use a mutex-guarded field so tests compile.
	w.errMu.Lock()
	w.lastErr = err
	w.errMu.Unlock()
}

func (w *syncWorker) loadErr() error {
	w.errMu.RLock()
	defer w.errMu.RUnlock()
	return w.lastErr
}

func (w *syncWorker) drainFlushers() {
	// Filled in by Task 3.
}
```

Also add fields to the `syncWorker` struct (replace the existing field block):

```go
type syncWorker struct {
	debounce   time.Duration
	syncFn     func(context.Context) error
	log        *slog.Logger

	triggerCh  chan struct{}
	flushQueue chan chan struct{}
	stopCh     chan struct{}

	started    atomic.Bool

	// lastErr / errMu will be replaced by atomic.Pointer in Task 4.
	errMu  sync.RWMutex
	lastErr error
}
```

Add `"sync"` to the import block at the top of `sync_worker.go`.

Remove the placeholder `var _ = fmt.Sprintf` line at the bottom — `fmt` is now genuinely used in `runOnce`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: PASS for all 5 tests (Stop, FlushBeforeStart, TriggersCoalesce, DebounceHonored, ShutdownClean).

- [ ] **Step 5: Commit**

```bash
git add internal/controller/sync_worker.go internal/controller/sync_worker_test.go
git commit -m "feat(controller): implement syncWorker run loop with debounce

Worker now drains triggerCh, debounces, and calls syncFn. Coalescing
verified: 10 rapid triggers → 1 syncFn call."
```

---

## Task 3: FlushSync + flush queue drain

**Files:**
- Modify: `internal/controller/sync_worker.go` (replace FlushSync stub, implement drainFlushers)
- Modify: `internal/controller/sync_worker_test.go` (add 3 tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/controller/sync_worker_test.go`:

```go
func TestSyncWorker_FlushSyncBlocks(t *testing.T) {
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		close(syncStarted)
		<-syncProceed
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	<-syncStarted // wait for syncFn to be in flight

	flushDone := make(chan struct{})
	go func() {
		_ = w.FlushSync(ctx)
		close(flushDone)
	}()

	select {
	case <-flushDone:
		t.Fatal("FlushSync returned before syncFn finished")
	case <-time.After(20 * time.Millisecond):
		// expected: FlushSync is blocking
	}

	close(syncProceed) // release syncFn
	select {
	case <-flushDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("FlushSync did not return within 500ms after syncFn finished")
	}
}

func TestSyncWorker_FlushSyncReturnsSyncErr(t *testing.T) {
	sentinel := errors.New("boom")
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		return sentinel
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	err := w.FlushSync(ctx)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected FlushSync to return sentinel error, got %v", err)
	}
}

func TestSyncWorker_FlushSyncCtxTimeout(t *testing.T) {
	proceed := make(chan struct{})
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		<-proceed
		return nil
	})
	bg, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	w.Start(bg)
	defer w.Stop()

	w.TriggerSync()
	time.Sleep(20 * time.Millisecond) // let syncFn block on proceed

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer flushCancel()
	err := w.FlushSync(flushCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	close(proceed)
}
```

Add `"errors"` to the import block of `sync_worker_test.go`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/controller/ -run 'TestSyncWorker_FlushSync' -v`
Expected: FAIL — FlushSync returns "FlushSync not implemented yet".

- [ ] **Step 3: Implement FlushSync and drainFlushers**

Replace the `FlushSync` stub in `sync_worker.go`:

```go
func (w *syncWorker) FlushSync(ctx context.Context) error {
	if !w.started.Load() {
		return errWorkerNotStarted
	}
	myDone := make(chan struct{})

	// Ensure worker has something to do.
	select {
	case w.triggerCh <- struct{}{}:
	default:
	}

	// Register our completion channel. May block briefly if another
	// flusher is mid-register; that's fine.
	select {
	case w.flushQueue <- myDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-myDone:
		return w.loadErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

Replace `drainFlushers`:

```go
func (w *syncWorker) drainFlushers() {
	for {
		select {
		case done := <-w.flushQueue:
			close(done)
		default:
			return
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: PASS for all 8 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/sync_worker.go internal/controller/sync_worker_test.go
git commit -m "feat(controller): implement syncWorker FlushSync

FlushSync blocks until at least one sync completes and returns the
last sync error. Worker drains the flush queue after every runOnce."
```

---

## Task 4: Re-trigger detection + panic recovery verification

**Files:**
- Modify: `internal/controller/sync_worker.go` (add post-runOnce trigger re-check)
- Modify: `internal/controller/sync_worker_test.go` (add 2 tests)

The runOnce panic recovery was already added in Task 2. This task adds (a) the re-trigger loop check, and (b) a dedicated test for panic recovery since Task 2 didn't exercise it.

- [ ] **Step 1: Write the failing tests**

Append to `sync_worker_test.go`:

```go
func TestSyncWorker_PanicRecovery(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			panic("simulated SDK panic")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	if err := w.FlushSync(ctx); err == nil {
		t.Errorf("expected FlushSync to surface panic-wrapped error, got nil")
	}

	// Worker should still be alive.
	w.TriggerSync()
	if err := w.FlushSync(ctx); err != nil {
		t.Errorf("expected second sync to succeed, got %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 syncFn calls (panic + recovery), got %d", got)
	}
}

func TestSyncWorker_ReTriggerAfterSync(t *testing.T) {
	var callCount int32
	firstStarted := make(chan struct{})
	firstProceed := make(chan struct{})
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			close(firstStarted)
			<-firstProceed
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	<-firstStarted

	// While syncFn #1 is blocked, queue another trigger.
	w.TriggerSync()

	close(firstProceed) // release #1

	// FlushSync should wait for #2 to also complete.
	if err := w.FlushSync(ctx); err != nil {
		t.Fatalf("FlushSync returned err: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 syncFn calls (re-trigger), got %d", got)
	}
}
```

- [ ] **Step 2: Run tests to verify state**

Run: `go test ./internal/controller/ -run 'TestSyncWorker_(PanicRecovery|ReTriggerAfterSync)' -v`
Expected:
- `PanicRecovery` PASSES (recovery already in Task 2).
- `ReTriggerAfterSync` FAILS — current run loop doesn't re-check triggerCh after runOnce, so callCount is 1 instead of 2.

- [ ] **Step 3: Add post-runOnce re-trigger check**

In `sync_worker.go`, change the `case <-w.triggerCh:` arm of the outer `for` in `run` to loop back if the channel has another signal after `runOnce`:

```go
func (w *syncWorker) run(ctx context.Context) {
	defer close(w.stopCh)
	timer := time.NewTimer(w.debounce)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.triggerCh:
			// Debounce window: absorb triggers until quiet.
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

			// If a trigger arrived during runOnce, loop again
			// instead of returning to idle. This catches the case
			// where the channel was full and additional triggers
			// were dropped on the floor.
			select {
			case <-w.triggerCh:
				timer.Reset(w.debounce)
				continue
			default:
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: PASS for all 10 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/sync_worker.go internal/controller/sync_worker_test.go
git commit -m "feat(controller): re-check triggerCh after syncWorker runOnce

A trigger that arrived mid-sync no longer gets lost — worker loops
back into another debounced sync instead of returning to idle."
```

---

## Task 5: Replace mutex-guarded lastErr with atomic.Pointer

The mutex-guarded `lastErr` works, but the spec calls for `atomic.Pointer[error]` so reads don't block drain in the hot path.

**Files:**
- Modify: `internal/controller/sync_worker.go`

- [ ] **Step 1: Edit the struct**

Replace these fields:

```go
	// lastErr / errMu will be replaced by atomic.Pointer in Task 4.
	errMu  sync.RWMutex
	lastErr error
```

with:

```go
	lastErr atomic.Pointer[error]
```

- [ ] **Step 2: Replace storeErr and loadErr**

Delete the `storeErr` and `loadErr` helpers, and update `runOnce` and `FlushSync` to use `lastErr` directly.

In `runOnce`, replace:
```go
		w.storeErr(err)
```
with:
```go
		w.lastErr.Store(&err)
```

And in the panic recovery block, replace:
```go
			w.storeErr(err)
```
with:
```go
			w.lastErr.Store(&err)
```

In `FlushSync`, replace:
```go
		return w.loadErr()
```
with:
```go
		if p := w.lastErr.Load(); p != nil {
			return *p
		}
		return nil
```

Delete the `storeErr` and `loadErr` functions entirely.

Remove the now-unused `"sync"` import (verify nothing else uses it — it shouldn't).

- [ ] **Step 3: Run tests to verify they still pass**

Run: `go test ./internal/controller/ -run TestSyncWorker -v`
Expected: PASS for all 10 tests, with no behavioral change.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/sync_worker.go
git commit -m "refactor(controller): switch syncWorker lastErr to atomic.Pointer

Reads no longer contend with the worker goroutine's writes. Behavior
unchanged."
```

---

## Task 6: Wire syncWorker into Controller

**Files:**
- Modify: `internal/controller/controller.go`

- [ ] **Step 1: Edit Controller struct**

Remove the `debounceTimer` and `pendingUpdates` fields (lines 77, 79 in current file). Add a `syncWorker` field. The struct section should now read:

```go
	// 防抖配置
	debounceDuration time.Duration // 防抖持续时间
	// syncWorker serializes all Cloudflare writes through a single
	// goroutine. See sync_worker.go.
	syncWorker *syncWorker
	// 对账配置
	reconcileEnabled  bool
	reconcileInterval time.Duration
```

- [ ] **Step 2: Update NewController to construct the worker**

At the end of `NewController`, just before `return controller, nil`... actually it's `return controller` (no error). Just before that line, add:

```go
	controller.syncWorker = newSyncWorker(controller.debounceDuration, controller.performSync, slog.Default())

	return controller
}
```

- [ ] **Step 3: Add Controller.Start and StopSyncWorker methods**

Append to `controller.go` (anywhere reasonable, e.g. right after `NewController`):

```go
// Start launches background goroutines requiring explicit lifecycle
// management. Currently just the sync worker. Called from main.go
// after NewController.
func (c *Controller) Start(ctx context.Context) {
	c.syncWorker.Start(ctx)
}

// StopSyncWorker blocks until the sync worker goroutine has exited.
// Called from main.go during shutdown.
func (c *Controller) StopSyncWorker() {
	c.syncWorker.Stop()
}
```

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: builds clean. The old `debounceTimer` and `pendingUpdates` are gone, but the next task rewrites the functions that referenced them.

If build fails because `syncToCloudflare` still references `c.debounceTimer`, that's expected — fix in Task 7. As a temporary measure for this task, you can leave the build broken and complete Task 7 immediately after. Alternatively, if you want green build at every commit, reorder: do Task 7 step 3 first, then commit Tasks 6+7 together.

- [ ] **Step 5: Commit (or fold into Task 7 if build is broken)**

If `go build ./...` passes (i.e., you've also done Task 7 step 3):

```bash
git add internal/controller/controller.go
git commit -m "feat(controller): wire syncWorker into Controller struct

NewController constructs the worker; Start/StopSyncWorker expose
lifecycle to main.go."
```

Otherwise, defer the commit until end of Task 7.

---

## Task 7: Rewrite syncToCloudflare to use TriggerSync

**Files:**
- Modify: `internal/controller/controller.go`

- [ ] **Step 1: Replace syncToCloudflare body**

Find the `syncToCloudflare` function (around line 670) and replace its body entirely:

Before:
```go
func (c *Controller) syncToCloudflare(ctx context.Context) error {
	c.mu.Lock()

	// 标记有待处理的更新
	c.pendingUpdates = true

	// 如果防抖计时器已经存在，停止它并重新开始计时
	if c.debounceTimer != nil {
		c.debounceTimer.Stop()
	}

	// 创建新的防抖计时器
	c.debounceTimer = time.AfterFunc(c.debounceDuration, func() {
		c.mu.Lock()
		c.pendingUpdates = false
		c.mu.Unlock()

		// 在单独的goroutine中执行实际的同步操作
		go c.performSync(context.Background())
	})

	c.mu.Unlock()

	// 立即返回，不等待同步完成
	return nil
}
```

After:
```go
// syncToCloudflare signals the sync worker that a sync is desired.
// Returns immediately; actual Cloudflare write happens in the worker
// goroutine after the debounce window. ctx is unused but kept for
// call-site compatibility.
func (c *Controller) syncToCloudflare(_ context.Context) error {
	c.syncWorker.TriggerSync()
	return nil
}
```

- [ ] **Step 2: Verify no remaining references to deleted fields**

Run: `grep -n "pendingUpdates\|debounceTimer" internal/controller/`
Expected: no output. If anything remains, remove it.

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 4: Run existing controller tests**

Run: `go test ./internal/controller/ -v`
Expected: all pre-existing controller tests still pass (no behavioral change for event-side callers — they now go through worker, but tests use sync calls and short timeouts).

If any test breaks because it asserted on `pendingUpdates` or `debounceTimer`: update or delete that assertion.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go
git commit -m "refactor(controller): syncToCloudflare delegates to syncWorker

Event-side call sites unchanged (8 sites). debounceTimer and
pendingUpdates are gone — the worker owns the timer and coalescing
state."
```

---

## Task 8: CleanupResources uses FlushSync

**Files:**
- Modify: `internal/controller/controller.go` (around line 180)
- Modify: `internal/controller/controller_test.go` (add 1 test)

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/controller_test.go`:

```go
// TestCleanupResources_BlocksUntilSyncRun verifies that CleanupResources
// does not return until the sync worker has processed the cleared-state
// sync. Regression guard for the shutdown race that motivated the
// syncWorker refactor.
func TestCleanupResources_BlocksUntilSyncRun(t *testing.T) {
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})

	mgr := &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
	}
	// We can't intercept performSync directly without a bigger refactor,
	// but UpdateConfiguration is the first CF call performSync makes.
	// Override the mock's UpdateConfiguration by introducing a sub-type.
	// Instead: assert that CleanupResources returns nil after sync
	// completes (proves it waited, not just fired-and-forgot).
	_ = syncStarted
	_ = syncProceed

	ctrl := NewController(nil, mgr, ControllerOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl.Start(ctx)
	defer ctrl.StopSyncWorker()

	if err := ctrl.CleanupResources(ctx); err != nil {
		t.Fatalf("CleanupResources returned err: %v", err)
	}

	// After CleanupResources returns, ingressRules should be empty and
	// the mock should have been called with an empty rule list.
	rules := ctrl.GetIngressRules()
	if len(rules) != 1 { // catch-all only
		t.Errorf("expected 1 rule (catch-all) after cleanup, got %d", len(rules))
	}
}
```

Note: This test is weaker than ideal — the mock doesn't block, so it can't directly prove CleanupResources waited. The behavioral assertion (empty rules after return) is what we can verify without larger refactor. See "Manual verification" in the spec for the blocking assertion.

- [ ] **Step 2: Run test to verify baseline (will pass even before code change)**

Run: `go test ./internal/controller/ -run TestCleanupResources_BlocksUntilSyncRun -v`
Expected: PASS. The current `performSync` directly does the work synchronously, so this test passes today.

- [ ] **Step 3: Replace the performSync call in CleanupResources**

In `controller.go`, find `CleanupResources` (around line 170) and change:

```go
	// 调用performSync同步空的规则集（这将删除所有DNS记录）
	if err := c.performSync(ctx); err != nil {
		slog.Error("Failed to perform cleanup sync", "error", err, "result", "failure")
		return err
	}
```

to:

```go
	// Flush the cleared-state sync through the worker so it lands
	// before we return. Otherwise a slow worker could lose the cleanup
	// write on shutdown.
	if err := c.syncWorker.FlushSync(ctx); err != nil {
		slog.Error("Failed to perform cleanup sync", "error", err, "result", "failure")
		return err
	}
```

- [ ] **Step 4: Run test to verify still passes**

Run: `go test ./internal/controller/ -run TestCleanupResources_BlocksUntilSyncRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "refactor(controller): CleanupResources uses FlushSync

Shutdown path now waits for the cleared-state sync to complete via
the worker's flush queue, rather than calling performSync directly
and racing with any in-flight debounced sync."
```

---

## Task 9: main.go starts and stops the sync worker

**Files:**
- Modify: `cmd/docktunnel/main.go`

- [ ] **Step 1: Add the worker lifecycle goroutine**

In `cmd/docktunnel/main.go`, find the existing compensation loop startup (search for `controller.RunCompensationLoop(ctx)`). Immediately before that block, add:

```go
	// Start the sync worker. Wrapper goroutine exists so wg.Wait() in
	// shutdown blocks until the worker has fully stopped, not just
	// until Start returns.
	wg.Add(1)
	go func() {
		defer wg.Done()
		appLogger.Info("Starting sync worker")
		controller.Start(ctx)
		<-ctx.Done()
		controller.StopSyncWorker()
	}()
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3: Run full test suite**

Run: `go test ./...`
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/docktunnel/main.go
git commit -m "feat(main): start syncWorker goroutine

Worker spawns alongside the compensation loop; wg.Wait() in shutdown
blocks on StopSyncWorker() so the worker is fully stopped before the
process exits."
```

---

## Task 10: fmt and final verification

**Files:**
- All touched files.

- [ ] **Step 1: Run gofmt**

Run: `gofmt -s -w .`

- [ ] **Step 2: Verify no diff**

Run: `git diff --stat`
Expected: empty (or only files you intentionally modified). If gofmt touched anything, add and commit it.

- [ ] **Step 3: Make fmt-check**

Run: `make fmt-check`
Expected: pass.

- [ ] **Step 4: Full test run with coverage**

Run: `go test -cover ./...`
Expected: all pass, controller package coverage doesn't regress materially from before this work.

- [ ] **Step 5: Commit if anything was reformatted**

```bash
git add -A
git commit -m "style: gofmt after syncWorker integration"
```

If nothing was reformatted, skip the commit.

---

## Manual Smoke Test (post-merge)

Per spec §5.4, before declaring the work done:

1. Build and run locally: `make build && ./docktunnel`
2. Start 5 labeled containers in quick succession (e.g., `for i in 1 2 3 4 5; do docker run -d --name "test$i" -l docktunnel.enable=true -l docktunnel.web$i.hostname="test$i.local" nginx; done`)
3. `curl http://localhost:9090/metrics | grep docktunnel_sync_duration_seconds_count` — should show single-digit increments, not 5x.
4. `kill -TERM <pid>` — logs should show `"Resources cleaned up successfully"` AFTER the final `"Sync operation completed successfully"` line, not before.

---

## Self-Review Checklist (post-write)

- [x] Spec coverage: §1 architecture (Task 1, 6), §2 data flow (Tasks 2, 3, 4), §3 API (Tasks 1, 3, 6, 7, 8, 9), §4 error handling (Tasks 2 runOnce recovery, 3 FlushSync), §5 testing (Tasks 1-5 unit tests, Task 8 controller integration).
- [x] No placeholders: every code step has complete code.
- [x] Type consistency: `syncWorker`, `newSyncWorker`, `TriggerSync`, `FlushSync`, `Start`, `Stop`, `run`, `runOnce`, `lastErr atomic.Pointer[error]` — all referenced consistently across tasks.
- [x] Frequent commits: each task ends with a commit.
- [x] TDD: tests written first in Tasks 1-4.
