package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

	triggerCh  chan struct{}      // size 1; signals "sync wanted"
	flushQueue chan chan struct{} // unbuffered; FlushSync registers its done chan
	stopCh     chan struct{}      // closed when run() exits

	started atomic.Bool

	// lastErr / errMu will be replaced by atomic.Pointer in Task 5.
	errMu   sync.RWMutex
	lastErr error
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
	// Will be replaced by atomic.Pointer store in Task 5.
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
	for {
		select {
		case done := <-w.flushQueue:
			close(done)
		default:
			return
		}
	}
}
