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
	debounce time.Duration
	syncFn   func(context.Context) error
	log      *slog.Logger
	// onError, when set, is invoked with the syncFn error so callers can
	// compensate (e.g. enqueue an ActionSync for the compensation queue,
	// B3). Not called for successful syncs.
	onError func(error)

	triggerCh chan chan struct{} // size 1; nil = trigger, non-nil = flush
	stopCh    chan struct{}      // closed when run() exits

	started atomic.Bool

	lastErr atomic.Pointer[error]
}

func newSyncWorker(debounce time.Duration, syncFn func(context.Context) error, log *slog.Logger) *syncWorker {
	return &syncWorker{
		debounce:  debounce,
		syncFn:    syncFn,
		log:       log,
		triggerCh: make(chan chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
}

// SetOnError installs the error callback invoked when a sync cycle fails.
func (w *syncWorker) SetOnError(fn func(error)) {
	w.onError = fn
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
	case w.triggerCh <- nil:
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
	myDone := make(chan struct{})

	// Single atomic operation: trigger AND register flusher in one
	// channel send. This closes the race where the worker could
	// complete a sync cycle between a separate trigger-send and
	// flusher-register, leaving the flusher hanging.
	select {
	case w.triggerCh <- myDone:
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

// run is the worker goroutine entry point. It debounces triggers and
// dispatches syncFn calls serially.
func (w *syncWorker) run(ctx context.Context) {
	defer close(w.stopCh)
	timer := time.NewTimer(w.debounce)
	timer.Stop()
	defer timer.Stop()

	for {
		var heldFlushers []chan struct{}
		select {
		case <-ctx.Done():
			return
		case f := <-w.triggerCh:
			if f != nil {
				heldFlushers = append(heldFlushers, f)
			}
		}

		timer.Reset(w.debounce)
	debounce:
		for {
			select {
			case <-ctx.Done():
				// Best-effort: close any held flushers on shutdown.
				for _, f := range heldFlushers {
					close(f)
				}
				return
			case f := <-w.triggerCh:
				if f != nil {
					heldFlushers = append(heldFlushers, f)
				}
				timer.Reset(w.debounce)
			case <-timer.C:
				break debounce
			}
		}
		w.runOnce(ctx, heldFlushers)
	}
}

// runOnce invokes syncFn, stores the result, and drains any pending
// flushers. The panic recovery keeps a single bad sync from killing
// the worker.
func (w *syncWorker) runOnce(ctx context.Context, heldFlushers []chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("syncFn panic recovered", "panic", r)
			err := fmt.Errorf("sync panicked: %v", r)
			w.lastErr.Store(&err)
			if w.onError != nil {
				w.onError(err)
			}
		}
		// Close all flushers (held + queued) so callers unblock on
		// both panic and normal paths.
		for _, f := range heldFlushers {
			close(f)
		}
	}()
	err := w.syncFn(ctx)
	w.lastErr.Store(&err)
	if err != nil {
		w.log.Warn("sync failed; will retry on next trigger", "error", err)
		if w.onError != nil {
			w.onError(err)
		}
	}
}
